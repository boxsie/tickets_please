// Package sse holds the Server-Sent-Events wire format for the web UI and the
// Datastar patch-frame builders the realtime handler emits.
//
// The realtime fan-out itself lives in internal/eventbus (typed events, topic
// scoping, replay). This package is purely the presentation seam: it turns a
// rendered HTML fragment (or a signal payload) into the exact SSE frame
// Datastar expects, and the web handler writes those frames to the wire.
package sse

import (
	"fmt"
	"io"
	"strings"
)

// Datastar SSE event names. Datastar v1 multiplexes everything over two event
// types: element patches (morph/append/replace HTML by selector) and signal
// patches (merge JSON into the client signal store).
const (
	EventPatchElements = "datastar-patch-elements"
	EventPatchSignals  = "datastar-patch-signals"
)

// Patch modes for EventPatchElements. Datastar defaults to "outer" (morph the
// matched element); "append"/"prepend" insert relative to it without
// disturbing siblings — the right fit for streaming a comment into a thread.
const (
	ModeOuter   = "outer"
	ModeInner   = "inner"
	ModeAppend  = "append"
	ModePrepend = "prepend"
	ModeRemove  = "remove"
)

// Event is one SSE message to deliver downstream. Fields map onto the SSE
// framing Write emits:
//
//	id:    <ID>          (omitted when empty)
//	event: <Type>        (omitted when empty — clients default to "message")
//	data:  <each line of Data>
//
// Data is the already-formatted payload body (Datastar's `elements …` /
// `mode …` / `selector …` lines, or `signals …`). Use the builders below
// rather than hand-rolling Data.
type Event struct {
	Type string
	Data string
	ID   string
}

// PatchElements builds a datastar-patch-elements frame that morphs html into
// the DOM. selector is optional — when empty, Datastar matches by the
// element's own id attribute. mode is one of the Mode* constants ("" defaults
// to outer/morph).
func PatchElements(selector, mode, html string) Event {
	var b strings.Builder
	if mode != "" {
		fmt.Fprintf(&b, "mode %s\n", mode)
	}
	if selector != "" {
		fmt.Fprintf(&b, "selector %s\n", selector)
	}
	// Each line of the fragment is sent as its own `elements` continuation so
	// multi-line HTML survives the SSE data framing.
	for i, line := range strings.Split(html, "\n") {
		if i == 0 {
			fmt.Fprintf(&b, "elements %s", line)
		} else {
			fmt.Fprintf(&b, "\nelements %s", line)
		}
	}
	return Event{Type: EventPatchElements, Data: b.String()}
}

// PatchSignals builds a datastar-patch-signals frame merging the given JSON
// object into the client signal store.
func PatchSignals(json string) Event {
	return Event{Type: EventPatchSignals, Data: "signals " + json}
}

// Write serialises ev into the on-wire SSE frame: optional id:, optional
// event:, then one data: line per line of ev.Data, terminated by the required
// blank line.
func Write(w io.Writer, ev Event) {
	if ev.ID != "" {
		fmt.Fprintf(w, "id: %s\n", ev.ID)
	}
	if ev.Type != "" {
		fmt.Fprintf(w, "event: %s\n", ev.Type)
	}
	for _, line := range strings.Split(ev.Data, "\n") {
		fmt.Fprintf(w, "data: %s\n", line)
	}
	fmt.Fprint(w, "\n")
}

// WriteComment writes a `: <text>` keep-alive line. SSE comments are ignored
// by the EventSource API but keep the connection (and proxies) alive.
func WriteComment(w io.Writer, text string) {
	fmt.Fprintf(w, ": %s\n\n", text)
}
