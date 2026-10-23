package printer

import "strings"

// Kind is what a printer is for. It decides which formats make sense, which tools the
// panel offers (calibrating a label roll means nothing to a laser printer), and which
// test page it prints.
type Kind string

const (
	KindLabel    Kind = "label"    // thermal label printers (Zebra and ZPL-compatible)
	KindReceipt  Kind = "receipt"  // 58/80 mm thermal receipt printers (ESC/POS)
	KindDocument Kind = "document" // A4/Letter office printers (IPP, PCL, PostScript, ESC/P-R)
)

// Format is a document format: either a printer language sent as is, or a portable
// document the server converts.
type Format string

const (
	FormatZPL        Format = "zpl"
	FormatESCPOS     Format = "escpos"
	FormatPDF        Format = "pdf"
	FormatPCL        Format = "pcl"
	FormatPostScript Format = "postscript"
	FormatPWG        Format = "pwg" // PWG raster (IPP Everywhere)
	FormatURF        Format = "urf" // Apple raster (AirPrint)
	FormatPNG        Format = "png"
	FormatJPEG       Format = "jpeg"
	FormatESCPR      Format = "escpr" // Epson inkjet raster (ESC/P-R)
)

// Driver names, shared by the server's drivers and the agent's registrations.
const (
	DriverZebra  = "zebra9100"
	DriverESCPOS = "escpos9100"
	DriverIPP    = "ipp"
	DriverPDL    = "pdl9100"
	DriverESCPR  = "escpr9100"
	DriverMock   = "mock"
)

// KindForDriver is the kind a printer has when its config does not say.
func KindForDriver(driver string) Kind {
	switch driver {
	case DriverESCPOS:
		return KindReceipt
	case DriverIPP, DriverPDL, DriverESCPR:
		return KindDocument
	default:
		return KindLabel
	}
}

// DefaultFormats is what a printer of a kind accepts when nothing more is known.
func DefaultFormats(driver string) []Format {
	switch driver {
	case DriverESCPOS:
		return []Format{FormatESCPOS}
	case DriverIPP:
		return []Format{FormatPDF}
	case DriverPDL:
		return []Format{FormatPCL}
	case DriverESCPR:
		return []Format{FormatESCPR}
	default:
		return []Format{FormatZPL}
	}
}

// ParseFormat accepts a format name or a common alias (MIME type, "ps", "jpg").
func ParseFormat(s string) (Format, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "zpl", "application/vnd.zebra-zpl":
		return FormatZPL, true
	case "escpos", "esc/pos", "esc-pos", "application/vnd.escpos":
		return FormatESCPOS, true
	case "pdf", "application/pdf":
		return FormatPDF, true
	case "pcl", "application/vnd.hp-pcl":
		return FormatPCL, true
	case "postscript", "ps", "application/postscript":
		return FormatPostScript, true
	case "pwg", "image/pwg-raster":
		return FormatPWG, true
	case "urf", "image/urf":
		return FormatURF, true
	case "png", "image/png":
		return FormatPNG, true
	case "jpeg", "jpg", "image/jpeg":
		return FormatJPEG, true
	case "escpr", "esc/p-r", "esc-p-r", "application/vnd.epson.escpr":
		return FormatESCPR, true
	}
	return "", false
}

// MIME is the format's media type, as IPP and HTTP name it.
func (f Format) MIME() string {
	switch f {
	case FormatPDF:
		return "application/pdf"
	case FormatPCL:
		return "application/vnd.hp-pcl"
	case FormatPostScript:
		return "application/postscript"
	case FormatPWG:
		return "image/pwg-raster"
	case FormatURF:
		return "image/urf"
	case FormatPNG:
		return "image/png"
	case FormatJPEG:
		return "image/jpeg"
	}
	return "application/octet-stream"
}

// IsImage reports whether f is a picture the server can decode.
func (f Format) IsImage() bool { return f == FormatPNG || f == FormatJPEG }
