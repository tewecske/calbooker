package httpadapter

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tewecske/calbooker/internal/locale"
	"github.com/tewecske/calbooker/internal/service"
)

// ParticipationUseCases aggregates the sign-up and attendance operations
// consumed by HTTP handlers.
type ParticipationUseCases interface {
	Roster(ctx context.Context, access *service.GroupAccess, detail service.EventDetail) (service.ParticipationRoster, error)
	Respond(ctx context.Context, actor service.GroupActor, eventID, userID int64, status string) (*service.Participant, error)
	RecordAttendance(ctx context.Context, actor service.GroupActor, eventID int64, marks []service.AttendanceMark) error
	// CalendarAnswers returns the viewer's own answers and their children's
	// for the events overlapping [from, to) (#30).
	CalendarAnswers(ctx context.Context, access *service.GroupAccess, from, to time.Time) ([]service.CalendarAnswer, error)
}

// ParticipationView is the participants card of an event page.
type ParticipationView struct {
	Phase              string
	PhaseLabel         string
	Summary            string
	RespondURL         string
	AttendanceURL      string
	CanManage          bool
	ShowEmails         bool
	AttendanceOpen     bool
	AttendanceRecorded bool
	Entries            []ParticipantView
	WalkIns            []SelectOptionView
}

// ParticipantView is one player on an event's participant list.
type ParticipantView struct {
	UserID      int64
	Label       string
	Email       string
	Guest       bool
	Profile     bool
	Self        bool
	Child       bool
	OnEventTeam bool
	Status      string
	StatusLabel string
	// Attendance is "yes", "no", or "" (not confirmed).
	Attendance string
	// AttendanceChecked prefills the attendance form: the confirmed value,
	// else whether the player was coming.
	AttendanceChecked bool
	Choices           []ParticipantChoiceView
	CanRemove         bool
	RemoveLabel       string
}

// ParticipantChoiceView is one answer button.
type ParticipantChoiceView struct {
	Status    string
	Label     string
	AriaLabel string
	Primary   bool
}

// registerParticipationRoutes registers the sign-up and attendance writes of
// each event page.
func registerParticipationRoutes(mux *http.ServeMux, groups *groupHandler) {
	for _, prefix := range localizedPrefixes() {
		mux.HandleFunc("POST "+prefix+"/groups/{group}/events/{eventID}/participation", groups.respondToEvent)
		mux.HandleFunc("POST "+prefix+"/groups/{group}/events/{eventID}/attendance", groups.recordAttendance)
	}
}

func (h *groupHandler) respondToEvent(writer http.ResponseWriter, request *http.Request) {
	current, eventID, ok := h.participationRequest(writer, request)
	if !ok {
		return
	}
	_ = request.ParseForm()
	userID, err := strconv.ParseInt(strings.TrimSpace(request.PostFormValue("user_id")), 10, 64)
	if err != nil || userID <= 0 {
		h.renderWriteError(writer, request, current, groupSectionEvent, eventID, service.ErrParticipantNotEligible, FormData{})
		return
	}
	status := strings.TrimSpace(request.PostFormValue("status"))
	if status == "none" {
		status = ""
	} else if status == "" {
		status = "invalid"
	}
	if _, err := h.dependencies.Participation.Respond(request.Context(), current.actor(), eventID, userID, status); err != nil {
		h.renderWriteError(writer, request, current, groupSectionEvent, eventID, err, FormData{})
		return
	}
	h.renderSection(writer, request, current, groupSectionEvent, eventID, FormData{}, []Alert{{Level: "success", Message: h.t(current.language, "participation.saved")}}, http.StatusOK)
}

func (h *groupHandler) recordAttendance(writer http.ResponseWriter, request *http.Request) {
	current, eventID, ok := h.participationRequest(writer, request)
	if !ok {
		return
	}
	_ = request.ParseForm()
	listed, _ := formIDs(request.PostForm["listed"])
	attended, _ := formIDs(request.PostForm["attended"])
	walkIn := optionalFormNumber(strings.TrimSpace(request.PostFormValue("walk_in")))
	present := make(map[int64]bool, len(attended))
	for _, id := range attended {
		present[id] = true
	}
	marks := make([]service.AttendanceMark, 0, len(listed)+1)
	seen := make(map[int64]bool, len(listed)+1)
	for _, id := range listed {
		if seen[id] {
			continue
		}
		seen[id] = true
		marks = append(marks, service.AttendanceMark{UserID: id, Attended: present[id]})
	}
	for _, id := range attended {
		if !seen[id] {
			// A checked box must belong to a listed player.
			h.renderWriteError(writer, request, current, groupSectionEvent, eventID, service.ErrInvalidAttendance, FormData{})
			return
		}
	}
	switch {
	case walkIn < 0:
		h.renderWriteError(writer, request, current, groupSectionEvent, eventID, service.ErrInvalidAttendance, FormData{})
		return
	case walkIn > 0 && !seen[walkIn]:
		marks = append(marks, service.AttendanceMark{UserID: walkIn, Attended: true})
	}
	if err := h.dependencies.Participation.RecordAttendance(request.Context(), current.actor(), eventID, marks); err != nil {
		h.renderWriteError(writer, request, current, groupSectionEvent, eventID, err, FormData{})
		return
	}
	h.renderSection(writer, request, current, groupSectionEvent, eventID, FormData{}, []Alert{{Level: "success", Message: h.t(current.language, "participation.attendance_saved")}}, http.StatusOK)
}

func (h *groupHandler) participationRequest(writer http.ResponseWriter, request *http.Request) (groupRequest, int64, bool) {
	current, eventID, ok := h.eventPathRequest(writer, request)
	if !ok {
		return groupRequest{}, 0, false
	}
	if h.dependencies.Participation == nil {
		h.renderError(writer, request, http.StatusInternalServerError)
		return groupRequest{}, 0, false
	}
	return current, eventID, true
}

// loadParticipation fills the participants card of the event page. Without
// the participation use cases the page shows no card.
func (h *groupHandler) loadParticipation(writer http.ResponseWriter, request *http.Request, current groupRequest, detail service.EventDetail, page *EventDetailView) bool {
	if h.dependencies.Participation == nil {
		return true
	}
	language := current.language
	roster, err := h.dependencies.Participation.Roster(request.Context(), current.access, detail)
	if err != nil {
		h.renderEventLoadError(writer, request, language, err)
		return false
	}
	page.Participation = h.participationView(language, page.URL, roster)
	return true
}

func (h *groupHandler) participationView(language locale.Code, eventURL string, roster service.ParticipationRoster) *ParticipationView {
	view := &ParticipationView{
		Phase:              roster.Phase,
		PhaseLabel:         h.t(language, "participation.phase."+roster.Phase),
		RespondURL:         eventURL + "/participation",
		AttendanceURL:      eventURL + "/attendance",
		CanManage:          roster.CanManage,
		ShowEmails:         roster.ShowEmails,
		AttendanceOpen:     roster.AttendanceOpen,
		AttendanceRecorded: roster.AttendanceRecorded,
	}
	if roster.Limit != nil {
		view.Summary = h.translate(language, "participation.summary_limit", map[string]string{"taken": strconv.Itoa(roster.Taken), "limit": strconv.Itoa(*roster.Limit)})
	} else {
		view.Summary = h.translate(language, "participation.summary", map[string]string{"taken": strconv.Itoa(roster.Taken)})
	}
	nominated := roster.Event.SignupMode == service.EventSignupNominated
	for _, entry := range roster.Entries {
		player := entry.Player
		row := ParticipantView{
			UserID:      player.UserID,
			Label:       h.memberLabel(language, player.UserID, player.DisplayName, player.Username, nil),
			Guest:       player.Kind == service.UserKindGuest,
			Profile:     player.Kind == service.UserKindProfile,
			Self:        entry.Self,
			Child:       entry.Child,
			OnEventTeam: entry.OnEventTeam,
			Status:      entry.Status,
			CanRemove:   entry.CanRemove,
		}
		if roster.ShowEmails && player.Email != nil {
			row.Email = *player.Email
		}
		switch {
		case entry.Status != "":
			row.StatusLabel = h.t(language, "participation.status."+entry.Status)
		case entry.Attended != nil:
			row.StatusLabel = h.t(language, "participation.status.walk_in")
		default:
			row.StatusLabel = h.t(language, "participation.status.none")
		}
		switch {
		case entry.Attended != nil && *entry.Attended:
			row.Attendance, row.AttendanceChecked = "yes", true
		case entry.Attended != nil:
			row.Attendance = "no"
		default:
			row.AttendanceChecked = entry.Status == service.ParticipantSignedUp || entry.Status == service.ParticipantNominated
		}
		who := "self"
		if roster.CanManage {
			who = "manage"
		}
		for _, status := range entry.Choices {
			label := h.t(language, "participation.choice."+who+"."+status)
			row.Choices = append(row.Choices, ParticipantChoiceView{
				Status:    status,
				Label:     label,
				AriaLabel: label + " – " + row.Label,
				Primary:   status != service.ParticipantDeclined,
			})
		}
		if nominated {
			row.RemoveLabel = h.t(language, "participation.remove_nomination")
		} else {
			row.RemoveLabel = h.t(language, "participation.remove")
		}
		view.Entries = append(view.Entries, row)
	}
	for _, player := range roster.WalkIns {
		view.WalkIns = append(view.WalkIns, SelectOptionView{Value: strconv.FormatInt(player.UserID, 10), Label: h.memberLabel(language, player.UserID, player.DisplayName, player.Username, nil)})
	}
	return view
}

// participationErrorMessage returns the message of a refused sign-up or
// attendance change, or "" for other errors.
func participationErrorMessage(err error) string {
	switch {
	case errors.Is(err, service.ErrParticipantNotEligible):
		return "participation.error.not_eligible"
	case errors.Is(err, service.ErrInvalidParticipantStatus):
		return "participation.error.invalid_status"
	case errors.Is(err, service.ErrParticipationCancelled):
		return "participation.error.cancelled"
	case errors.Is(err, service.ErrParticipationStarted):
		return "participation.error.started"
	case errors.Is(err, service.ErrSignupDeadlinePassed):
		return "participation.error.deadline"
	case errors.Is(err, service.ErrEventFull):
		return "participation.error.full"
	case errors.Is(err, service.ErrAttendanceTooEarly):
		return "participation.error.too_early"
	case errors.Is(err, service.ErrInvalidAttendance):
		return "participation.error.invalid_attendance"
	case errors.Is(err, service.ErrEventHasParticipants):
		return "participation.error.has_participants"
	}
	return ""
}
