package ui

import (
	"net/http"
	"time"
)

type activityRow struct {
	At    time.Time
	Type  string
	Node  string
	Model string
	Note  string
}

func (h *Handler) activity(w http.ResponseWriter, r *http.Request) {
	var rows []activityRow
	if h.Activity != nil {
		ev := h.Activity.List()
		rows = make([]activityRow, 0, len(ev))
		for _, e := range ev {
			rows = append(rows, activityRow{
				At:    e.At,
				Type:  string(e.Type),
				Node:  e.NodeID,
				Model: e.Model,
				Note:  e.Note,
			})
		}
	}

	vm := h.newViewModel("Activity")
	vm.Activity = rows
	h.render(w, "activity.html", vm)
}
