package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kucial/cloud-print-service/pkg/agentapi"
	"github.com/kucial/cloud-print-service/pkg/printer"
)

// The agent serves label (ZPL), receipt (ESC/POS), office (PCL/PostScript with PJL)
// and Epson inkjet (ESC/P-R) printers. Each speaks its own status protocol; this file is the one place that knows
// which, for both the USB and the local-share backends.

// errWriteOnly is returned for a query to a printer that has no way to answer (a USB
// interface without an IN endpoint).
var errWriteOnly = errors.New("printer is write-only: it cannot answer queries")

var zebraProfile = printer.Profile{Driver: printer.DriverZebra, Kind: printer.KindLabel,
	Formats: []printer.Format{printer.FormatZPL}}

// replyComplete reports whether acc holds the whole answer to cmd.
func replyComplete(cmd string, acc []byte) bool {
	q := []byte(cmd)
	switch {
	case printer.IsESCPOSStatusQuery(q):
		return len(acc) >= 1
	case printer.IsESCPOSInfoQuery(q):
		return printer.ESCPOSInfoComplete(acc)
	case printer.IsPJLQuery(q):
		return printer.PJLReplyComplete(acc)
	}
	return printer.ReplyComplete(cmd, acc)
}

// isServerQuery reports whether chunk, as the server's driver sent it, is a status or
// identity query for a printer of this profile. Those are answered by the agent (by
// asking the printer) and never written to the printer as document data: a PCL
// printer would print "@PJL INFO STATUS" as text.
//
// Receipt and office queries must match exactly, so a document that happens to start
// with the same bytes is never taken for one.
func isServerQuery(p printer.Profile, chunk []byte) bool {
	switch p.Driver {
	case printer.DriverESCPOS:
		return len(chunk) == 3 && (printer.IsESCPOSStatusQuery(chunk) || printer.IsESCPOSInfoQuery(chunk))
	case printer.DriverPDL:
		return bytes.Equal(chunk, printer.CmdPJLStatus) || bytes.Equal(chunk, printer.CmdPJLID) ||
			bytes.Equal(chunk, printer.CmdPJLConfig)
	case printer.DriverESCPR:
		return false // the server never asks an inkjet anything: every byte is the job
	}
	return printer.IsHostQuery(strings.TrimLeft(string(chunk), " \r\n\t"))
}

// statusFor asks the printer for its state in its own protocol. A printer that
// cannot say (no PJL, write-only) is reported reachable with an unknown state.
func statusFor(ctx context.Context, p printer.Profile, query func(context.Context, string) (string, error)) (agentapi.Status, error) {
	now := time.Now().UTC()
	offline := func(err error) (agentapi.Status, error) {
		return agentapi.Status{State: printer.StateOffline, ObservedAt: now, Detail: err.Error()}, err
	}
	unknown := func(why string) (agentapi.Status, error) {
		return agentapi.Status{State: printer.StateUnknown, ObservedAt: now, Detail: why}, nil
	}
	switch p.Driver {
	case printer.DriverESCPOS:
		var b [3]byte
		for i, cmd := range [][]byte{printer.CmdESCPOSPrinterStatus, printer.CmdESCPOSOfflineCause, printer.CmdESCPOSPaperStatus} {
			raw, err := query(ctx, string(cmd))
			switch {
			case errors.Is(err, errWriteOnly):
				return unknown("write-only printer: no status")
			case err != nil && (isTimeoutErr(err) || errors.Is(err, errNoReply)):
				// Opened, but silent: it prints, it just does not report.
				return unknown("printer does not answer ESC/POS status requests")
			case err != nil && i > 0:
				return unknown("printer answers only part of the ESC/POS status requests")
			case err != nil:
				return offline(err)
			}
			b[i] = raw[0]
		}
		st, ok := printer.ParseESCPOSStatus(b[0], b[1], b[2])
		if !ok {
			return unknown(fmt.Sprintf("unrecognised ESC/POS status % x", b))
		}
		return agentapi.Status{State: st.State(), Raw: fmt.Sprintf("% x", b), Detail: st.Detail(), ObservedAt: now}, nil

	case printer.DriverESCPR:
		// Nothing to ask that the printer would not risk printing.
		return unknown("ESC/P-R printers report no status")

	case printer.DriverPDL:
		if !p.PJL {
			return unknown("printer reports no status (no PJL)")
		}
		raw, err := query(ctx, string(printer.CmdPJLStatus))
		if errors.Is(err, errWriteOnly) {
			return unknown("write-only printer: no status")
		}
		if err != nil && (isTimeoutErr(err) || errors.Is(err, errNoReply)) {
			return unknown("printer does not answer PJL status")
		}
		if err != nil {
			return offline(err)
		}
		st, ok := printer.ParsePJLStatus([]byte(raw))
		if !ok {
			return unknown("unrecognised PJL status reply")
		}
		return agentapi.Status{State: st.State(), Raw: fmt.Sprint(st.Code), Detail: st.Detail(), ObservedAt: now}, nil
	}
	raw, err := query(ctx, printer.CmdHostStatus)
	if err != nil {
		return offline(err)
	}
	return ParseHostStatus(raw, now), nil
}

// identifyModel asks a receipt or office printer for its model, falling back to what
// its 1284 id says. Zebras are identified with ~HI by the caller.
func identifyModel(ctx context.Context, p printer.Profile, fallback string, query func(context.Context, string) (string, error)) string {
	switch {
	case p.Driver == printer.DriverESCPOS:
		if raw, err := query(ctx, string(printer.CmdESCPOSModelName)); err == nil {
			if m, ok := printer.ParseESCPOSInfo([]byte(raw)); ok {
				return m
			}
		}
	case p.Driver == printer.DriverPDL && p.PJL:
		if raw, err := query(ctx, string(printer.CmdPJLID)); err == nil {
			if m, ok := printer.ParsePJLID([]byte(raw)); ok {
				return m
			}
		}
	}
	return fallback
}
