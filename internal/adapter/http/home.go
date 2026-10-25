package httpadapter

import (
	"net/http"
	"strconv"

	"github.com/tewecske/calbooker/internal/locale"
	"github.com/tewecske/calbooker/internal/service"
)

// HomeView is the signed-in home page: the account's groups and the shortcuts
// to create or join one. It is nil for anonymous visitors.
type HomeView struct {
	Intro        string
	GroupsLabel  string
	GroupsEmpty  string
	Groups       []HomeGroupView
	GroupsURL    string
	JoinLabel    string
	JoinURL      string
	CreateLabel  string
	CreateURL    string
	AdminLabel   string
	AdminURL     string
	GroupsFailed string
}

// HomeGroupView is one group link on the home page.
type HomeGroupView struct {
	Name string
	URL  string
}

func translateOrEmpty(renderer *PageRenderer, language locale.Code, id string) string {
	text, _ := renderer.Translate(language, id)
	return text
}

// homeView builds the signed-in home content. Guests see their groups and the
// join link; creating a group needs a full account, so the link is omitted.
func homeView(renderer *PageRenderer, dependencies Dependencies, request *http.Request, language locale.Code, user service.User) *HomeView {
	t := func(id string) string { return translateOrEmpty(renderer, language, id) }
	view := &HomeView{
		Intro:       t("home.signed_in"),
		GroupsLabel: t("groups.mine"),
		GroupsEmpty: t("groups.empty"),
		GroupsURL:   mustLocalePath(language, "/groups"),
		JoinLabel:   t("groups.join"),
		JoinURL:     mustLocalePath(language, "/groups") + "#groups-join-heading",
	}
	if user.IsAdmin {
		view.GroupsLabel = t("groups.all")
		view.AdminLabel = t("home.admin")
		view.AdminURL = mustLocalePath(language, "/admin")
	}
	if user.IsFullUser() {
		view.CreateLabel = t("groups.create")
		view.CreateURL = mustLocalePath(language, "/groups") + "#groups-create-heading"
	}
	if dependencies.Groups == nil {
		return view
	}
	summaries, err := dependencies.Groups.List(request.Context(), user.ID)
	if err != nil {
		view.GroupsFailed = t("groups.error.generic")
		return view
	}
	for _, summary := range summaries {
		path, err := locale.Path(language, "/groups/"+strconv.FormatInt(summary.Group.ID, 10))
		if err != nil {
			continue
		}
		view.Groups = append(view.Groups, HomeGroupView{Name: summary.Group.Name, URL: path})
	}
	return view
}
