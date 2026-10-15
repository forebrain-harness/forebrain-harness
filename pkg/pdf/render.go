package pdf

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/png"
	"os"

	pdfium_errors "github.com/klippa-app/go-pdfium/errors"
	"github.com/klippa-app/go-pdfium/requests"
)

// Rendering budgets. A page rendered at DefaultDPI is about 1240x1754 pixels
// and 200-400 KiB of PNG; DefaultMaxPages bounds how much of a long document
// reaches the model in one read. Both are cost knobs: change them through
// Options rather than at a call site.
const (
	// DefaultMaxPages is the number of pages RenderFile renders when a caller
	// passes maxPages <= 0.
	DefaultMaxPages = 5
	// DefaultDPI is the resolution RenderFile renders at when Options.DPI is
	// unset.
	DefaultDPI = 150
)

// Page is one rendered page. Number is 1-based. Only pages that rendered
// successfully appear in RenderResult.Pages; a page beyond the render cap is
// reported by RenderResult.PageCount, never as an entry here, so a Page
// obtained from a RenderResult always has its image.
type Page struct {
	Number int    // 1-based page number
	PNG    []byte // the page rendered as PNG
	Width  int    // image width in pixels
	Height int    // image height in pixels
}

// RenderResult is the outcome of rendering a document.
type RenderResult struct {
	PageCount   int    // total pages in the document
	Pages       []Page // the pages that were rendered, in order
	RenderedMax int    // the cap this call used
}

// Options tunes one RenderFile call. DPI is per call; FontDirs is process
// wide — see its field comment for why.
type Options struct {
	// DPI is the render resolution of this call; <= 0 means DefaultDPI.
	DPI float64
	// FontDirs are the directories handed to PDFium's font loader. nil means
	// FontDirs($FOREBRAIN_HOME); an empty non-nil slice means no directories
	// (let PDFium use its platform defaults). PDFium is initialised once per
	// process, so this takes effect on the FIRST render only — a later call
	// with a different set is a no-op; it is per-call input to a process-wide
	// initialisation, not a per-call knob.
	FontDirs []string
}

// RenderFile renders up to maxPages pages of the PDF at path, starting at the
// 1-based page `start`. start <= 0 means page 1; maxPages <= 0 means
// DefaultMaxPages. The END of the range is clamped into the document (a
// requested page past the last one is simply not rendered); a START past the
// last page renders nothing — Pages is empty and PageCount still names the
// document's length, mirroring how a text read past EOF returns no lines.
// Callers that index Pages[0] must check len(Pages) > 0 first. A
// password-protected, damaged or empty document returns an error the model can
// act on.
func RenderFile(path string, start, maxPages int, opts Options) (RenderResult, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return RenderResult{}, fmt.Errorf("pdf: read %q: %w", path, err)
	}
	if start < 1 {
		start = 1
	}
	if maxPages <= 0 {
		maxPages = DefaultMaxPages
	}
	dpi := opts.DPI
	if dpi <= 0 {
		dpi = DefaultDPI
	}
	dirs := opts.FontDirs
	if dirs == nil {
		dirs = FontDirs(os.Getenv("FOREBRAIN_HOME"))
	}

	// Instance acquisition does not wait: the single-threaded pool hands one
	// out immediately, and page rendering itself is serialised by PDFium's
	// process-level mutex and CANNOT be cancelled — a cgo call cannot be
	// interrupted by a context. A pathological PDF therefore blocks every PDF
	// read in this process until its page finishes; that is a library
	// property, recorded here honestly rather than papered over with a
	// timeout that never fired.
	instance, err := renderPool(dirs).GetInstanceWithContext(context.Background())
	if err != nil {
		return RenderResult{}, fmt.Errorf("pdf: get pdfium instance: %w", err)
	}
	defer instance.Close()

	document, err := instance.OpenDocument(&requests.OpenDocument{File: &data})
	if err != nil {
		return RenderResult{}, openError(err)
	}
	defer instance.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: document.Document})

	count, err := instance.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: document.Document})
	if err != nil {
		return RenderResult{}, fmt.Errorf("pdf: page count: %w", err)
	}
	if count.PageCount == 0 {
		return RenderResult{}, errors.New("this PDF has no pages")
	}

	result := RenderResult{PageCount: count.PageCount, RenderedMax: maxPages}
	if start > count.PageCount {
		// A start past the last page is an empty read, not an error: the
		// caller still learns the document's true length from PageCount.
		return result, nil
	}
	for index := start - 1; index < start-1+maxPages && index < count.PageCount; index++ {
		rendered, err := instance.RenderPageInDPI(&requests.RenderPageInDPI{
			DPI:  int(dpi),
			Page: requests.Page{ByIndex: &requests.PageByIndex{Document: document.Document, Index: index}},
		})
		if err != nil {
			return RenderResult{}, fmt.Errorf("pdf: render page %d: %w", index+1, err)
		}
		img := rendered.Result.RenderedImage
		if img == nil {
			img = rendered.Result.Image
		}
		if img == nil {
			return RenderResult{}, fmt.Errorf("pdf: page %d produced no image", index+1)
		}
		var encoded bytes.Buffer
		if err := png.Encode(&encoded, img); err != nil {
			return RenderResult{}, fmt.Errorf("pdf: encode page %d: %w", index+1, err)
		}
		bounds := img.Bounds()
		result.Pages = append(result.Pages, Page{
			Number: index + 1,
			PNG:    encoded.Bytes(),
			Width:  bounds.Dx(),
			Height: bounds.Dy(),
		})
	}
	return result, nil
}

// openError turns a PDFium open failure into a message the model can act on:
// unlocking a password-protected file, or giving up on a damaged one.
func openError(err error) error {
	switch {
	case errors.Is(err, pdfium_errors.ErrPassword):
		return errors.New("this PDF is password-protected; unlock it first")
	case errors.Is(err, pdfium_errors.ErrFormat):
		return fmt.Errorf("this PDF could not be parsed (%v)", err)
	default:
		return fmt.Errorf("this PDF could not be opened (%v)", err)
	}
}
