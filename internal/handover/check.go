package handover

import (
	"fmt"
	"slices"
	"strings"
)

// Check is what `devtools server upgrade-check --json` prints: what a build
// can take over. The old image asks the new build before anything is
// frozen.
type Check struct {
	Handover []int    `json:"handover"` // schema versions it reads
	Screens  []string `json:"screens"`  // screen formats it reads, best first
	Gens     []int    `json:"gens"`     // protocol generations it speaks
	Version  string   `json:"version"`
	GOOS     string   `json:"goos"`
	GOARCH   string   `json:"goarch"`
	Engine   string   `json:"engine"`
}

// Choice is the result of the negotiation: the schema version the old
// image writes and the screen format of the panes.
type Choice struct {
	Schema int
	Screen string
}

// Incompatible is the error of Choose: the target cannot take over, said
// in words for the user.
type Incompatible struct{ Text string }

func (e *Incompatible) Error() string { return e.Text }

func incompatible(format string, args ...any) error {
	return &Incompatible{Text: fmt.Sprintf(format, args...)}
}

// Choose negotiates between this build (local) and the target: the highest
// schema version both list, the first screen format of local that the
// target reads, the same system, and every protocol generation of gens
// (the connected windows' and the requester's) spoken by the target, or
// those windows could not connect to it.
func Choose(local, target Check, gens []int) (Choice, error) {
	if target.GOOS != local.GOOS || target.GOARCH != local.GOARCH {
		return Choice{}, incompatible("it is built for %s/%s, this server runs on %s/%s", target.GOOS, target.GOARCH, local.GOOS, local.GOARCH)
	}
	schema := 0
	for _, v := range local.Handover {
		if slices.Contains(target.Handover, v) && v > schema {
			schema = v
		}
	}
	if schema == 0 {
		return Choice{}, incompatible("it reads handover versions %v, this server writes %v", target.Handover, local.Handover)
	}
	screen := ""
	for _, f := range local.Screens {
		if slices.Contains(target.Screens, f) {
			screen = f
			break
		}
	}
	if screen == "" {
		return Choice{}, incompatible("it reads screens as %s, this server writes %s", list(target.Screens), list(local.Screens))
	}
	var missing []int
	for _, g := range gens {
		if !slices.Contains(target.Gens, g) && !slices.Contains(missing, g) {
			missing = append(missing, g)
		}
	}
	if len(missing) > 0 {
		return Choice{}, incompatible("it does not speak protocol %v, which connected windows use", missing)
	}
	return Choice{Schema: schema, Screen: screen}, nil
}

func list(s []string) string {
	if len(s) == 0 {
		return "nothing"
	}
	return strings.Join(s, ", ")
}

// Sample is what the target read from a sample screen of the chosen format
// (upgrade-check --sample): the old image wrote it and compares.
type Sample struct {
	Title string `json:"title"` // after the unfinished title was completed
	Paste bool   `json:"paste"` // mode 2004
	Alt   bool   `json:"alt"`
	Row   string `json:"row"` // the text of row 0
}
