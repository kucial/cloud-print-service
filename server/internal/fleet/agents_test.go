package fleet

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kucial/cloud-print-service/pkg/agentapi"
	"github.com/kucial/cloud-print-service/pkg/printer"
)

// fillAll sets every field of v to a distinct non-zero value, so a field that a
// conversion forgets shows up as a zero on the other side.
func fillAll(t *testing.T, v reflect.Value) {
	t.Helper()
	for i := 0; i < v.NumField(); i++ {
		if strings.HasPrefix(v.Type().Field(i).Name, "Legacy") {
			continue // accepted for compatibility, never carried over
		}
		f := v.Field(i)
		switch f.Kind() {
		case reflect.String:
			f.SetString(v.Type().Field(i).Name)
		case reflect.Int:
			f.SetInt(int64(i + 1))
		case reflect.Float64:
			f.SetFloat(float64(i) + 0.5)
		case reflect.Bool:
			f.SetBool(true)
		case reflect.Slice:
			f.Set(reflect.ValueOf([]string{v.Type().Field(i).Name}))
		default:
			t.Fatalf("fillAll: unhandled field %s of kind %s", v.Type().Field(i).Name, f.Kind())
		}
	}
}

// Every field an agent can report must survive the trip into the server model and back
// out through its JSON, or the contract and the model have drifted apart.
func TestPrinterFromAgentKeepsEveryField(t *testing.T) {
	var in agentapi.Printer
	fillAll(t, reflect.ValueOf(&in).Elem())

	b, err := json.Marshal(PrinterFromAgent(in))
	if err != nil {
		t.Fatal(err)
	}
	var out agentapi.Printer
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	// The server's live fields come back out under the legacy names; they are not part
	// of what the agent reported.
	out.LegacyUsable, out.LegacyQueueDepth, out.LegacyStatus = nil, nil, nil
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("fields lost in conversion:\n in: %+v\nout: %+v", in, out)
	}
}

func TestStatusFromAgentKeepsObservation(t *testing.T) {
	at := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	in := agentapi.Status{State: printer.StateMediaOut, Raw: "raw", Detail: "detail", ObservedAt: at}
	got := StatusFromAgent(in)
	if got.State != in.State || got.Raw != in.Raw || got.Detail != in.Detail || !got.ObservedAt.Equal(at) {
		t.Fatalf("status not carried over: %+v", got)
	}
	if got.Stale || got.LastJob != nil {
		t.Fatalf("server bookkeeping must not come from an agent: %+v", got)
	}
}
