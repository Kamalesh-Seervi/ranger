// Package classify infers what a device physically is from the traces it
// leaves on the air.
//
// Nothing here is certain. Every signal is a hint with a weight, hints are
// summed per category, and the winner is reported with a confidence that
// shrinks when a second category scores nearly as well.
package classify

import (
	"math"
	"sort"
	"strings"
)

// Category is the kind of physical object a device appears to be.
type Category string

const (
	Unknown    Category = "unknown"
	Router     Category = "router"
	Phone      Category = "phone"
	Computer   Category = "computer"
	TV         Category = "tv"
	Speaker    Category = "speaker"
	Headphones Category = "headphones"
	Printer    Category = "printer"
	Wearable   Category = "wearable"
	Tracker    Category = "tracker"
	Camera     Category = "camera"
	SmartHome  Category = "smart-home"
	Console    Category = "console"
	Beacon     Category = "beacon"
	Peripheral Category = "peripheral"
	Vehicle    Category = "vehicle"
)

// Mobility separates infrastructure that never moves from devices a person
// carries around. It is derived from how much the signal wanders.
type Mobility string

const (
	MobilityUnknown  Mobility = "unknown"
	MobilityFixed    Mobility = "fixed"
	MobilityPortable Mobility = "portable"
)

// Signals is everything the classifier is allowed to look at. It uses plain
// types so this package stays a dependency-free leaf.
type Signals struct {
	Kind     string
	Name     string
	Vendor   string
	Security string
	Meta     map[string]string
	// RSSISpread is the p90 minus p10 signal range in dB. A wide spread means
	// the device, or the observer, is moving.
	RSSISpread int
	Samples    int
}

// Result is the classifier's verdict.
type Result struct {
	Category   Category `json:"category"`
	Confidence float64  `json:"confidence"`
	Mobility   Mobility `json:"mobility"`
	Evidence   []string `json:"evidence,omitempty"`
}

// maxEvidence bounds what is retained per device so a chatty advertiser cannot
// grow the registry without limit.
const maxEvidence = 6

// minCommitScore is the weight needed before a category is claimed at all.
// It is set above the mobility nudges, so "this thing sits still" can sharpen
// a guess but can never be the whole basis for one.
const minCommitScore = 1.0

type scorer struct {
	scores   map[Category]float64
	evidence []string
}

func (s *scorer) bump(c Category, weight float64) { s.scores[c] += weight }

func (s *scorer) note(reason string) {
	if len(s.evidence) >= maxEvidence {
		return
	}
	for _, existing := range s.evidence {
		if existing == reason {
			return
		}
	}
	s.evidence = append(s.evidence, reason)
}

// Classify weighs every available hint and returns the most likely category.
func Classify(s Signals) Result {
	sc := &scorer{scores: make(map[Category]float64, 8)}
	mobility := mobilityOf(s)

	meta := s.Meta
	if meta == nil {
		meta = map[string]string{}
	}

	if s.Kind == "wifi-ap" {
		sc.note("broadcasts a Wi-Fi network")
		sc.bump(Router, 3)
	}

	applyService(sc, strings.ToLower(meta["service"]))
	applyApple(sc, meta["apple"])
	applyBLEServices(sc, meta["services"])
	applyKeywords(sc, strings.ToLower(s.Vendor), vendorRules, "vendor")
	applyKeywords(sc, strings.ToLower(s.Name), nameRules, "name")
	applyMobility(sc, mobility, s.Kind)

	category, confidence := best(sc.scores)
	return Result{
		Category:   category,
		Confidence: confidence,
		Mobility:   mobility,
		Evidence:   sc.evidence,
	}
}

// Spread returns the p90 minus p10 range of a signal series, which is more
// robust than the full range when a single reflection causes one bad reading.
func Spread(samples []int) int {
	if len(samples) < 2 {
		return 0
	}
	sorted := make([]int, len(samples))
	copy(sorted, samples)
	sort.Ints(sorted)

	low := sorted[len(sorted)/10]
	high := sorted[len(sorted)-1-len(sorted)/10]
	if high < low {
		return 0
	}
	return high - low
}

// mobilityOf reads movement out of signal variance. Access points are
// infrastructure by definition and are never treated as portable.
func mobilityOf(s Signals) Mobility {
	if s.Kind == "wifi-ap" {
		return MobilityFixed
	}
	if s.Samples < 8 {
		return MobilityUnknown
	}
	switch {
	case s.RSSISpread <= 5:
		return MobilityFixed
	case s.RSSISpread >= 12:
		return MobilityPortable
	default:
		return MobilityUnknown
	}
}

// applyMobility nudges categories associated with sitting still or being
// carried. The weights stay small because movement is only a weak hint.
func applyMobility(sc *scorer, mobility Mobility, kind string) {
	if kind == "wifi-ap" {
		return
	}
	switch mobility {
	case MobilityFixed:
		sc.note("signal is steady, so it is not being carried")
		sc.bump(SmartHome, 0.6)
		sc.bump(Speaker, 0.4)
		sc.bump(TV, 0.4)
		sc.bump(Printer, 0.3)
	case MobilityPortable:
		sc.note("signal wanders, so it is moving")
		sc.bump(Phone, 0.6)
		sc.bump(Wearable, 0.5)
		sc.bump(Headphones, 0.4)
		sc.bump(Tracker, 0.3)
	}
}

func applyService(sc *scorer, service string) {
	if service == "" {
		return
	}
	for _, rule := range serviceRules {
		if strings.HasPrefix(service, rule.needle) {
			sc.note("advertises " + rule.needle)
			sc.bump(rule.cat, rule.weight)
		}
	}
}

// applyApple reads Apple's continuity advertisement subtype, which reveals
// what a nearby Apple device is doing even when it refuses to give a name.
func applyApple(sc *scorer, kind string) {
	if kind == "" {
		return
	}
	sc.note("Apple continuity: " + kind)
	switch kind {
	case "Proximity Pairing":
		sc.bump(Headphones, 2.5)
	case "Find My":
		// Offline finding is broadcast by AirTags and by idle iPhones and Macs
		// alike, so it only weakly implies a tracker.
		sc.bump(Tracker, 1.4)
		sc.bump(Phone, 0.6)
	case "iBeacon":
		sc.bump(Beacon, 2.0)
	case "AirPlay":
		sc.bump(TV, 1.5)
		sc.bump(Speaker, 1.0)
	case "Nearby", "Handoff", "AirDrop":
		sc.bump(Phone, 1.2)
		sc.bump(Computer, 1.0)
	}
}

func applyBLEServices(sc *scorer, services string) {
	if services == "" {
		return
	}
	for _, raw := range strings.Split(services, ",") {
		short := shortUUID(raw)
		if short == "" {
			continue
		}
		for _, rule := range bleServiceRules {
			if short == rule.needle {
				sc.note("BLE " + rule.label)
				sc.bump(rule.cat, rule.weight)
			}
		}
	}
}

func applyKeywords(sc *scorer, haystack string, rules []keywordRule, label string) {
	if haystack == "" {
		return
	}
	for _, rule := range rules {
		if strings.Contains(haystack, rule.needle) {
			sc.note(label + " mentions " + rule.needle)
			sc.bump(rule.cat, rule.weight)
		}
	}
}

// shortUUID reduces a Bluetooth SIG 128-bit UUID to its 16-bit assigned form.
func shortUUID(raw string) string {
	u := strings.ToLower(strings.TrimSpace(raw))
	switch {
	case len(u) == 4:
		return u
	case len(u) == 36 && strings.HasPrefix(u, "0000"):
		return u[4:8]
	default:
		return ""
	}
}

// best picks the winner and scales confidence down when the runner-up is close.
func best(scores map[Category]float64) (Category, float64) {
	names := make([]string, 0, len(scores))
	for c := range scores {
		names = append(names, string(c))
	}
	// Sorted iteration keeps the outcome stable when two categories tie.
	sort.Strings(names)

	var top, second float64
	winner := Unknown
	for _, name := range names {
		value := scores[Category(name)]
		switch {
		case value > top:
			second = top
			top, winner = value, Category(name)
		case value > second:
			second = value
		}
	}
	if top < minCommitScore {
		return Unknown, 0
	}
	confidence := top / (top + second + 1.5)
	return winner, math.Round(confidence*100) / 100
}

type keywordRule struct {
	needle string
	cat    Category
	weight float64
}

// serviceRules map DNS-SD service types to the device that offers them. These
// are the strongest signal available: a device only advertises a service it
// actually implements.
var serviceRules = []keywordRule{
	{"_googlecast", TV, 2.5},
	{"_androidtvremote", TV, 3.0},
	{"_amzn-wplay", TV, 2.5},
	{"_roku", TV, 3.0},
	{"_airplay", TV, 2.0},
	{"_raop", Speaker, 2.0},
	{"_sonos", Speaker, 3.0},
	{"_spotify-connect", Speaker, 2.0},
	{"_ipps", Printer, 3.0},
	{"_ipp", Printer, 3.0},
	{"_printer", Printer, 3.0},
	{"_pdl-datastream", Printer, 3.0},
	{"_uscan", Printer, 2.5},
	{"_scanner", Printer, 2.5},
	{"_hap", SmartHome, 3.0},
	{"_matter", SmartHome, 3.0},
	{"_homekit", SmartHome, 3.0},
	{"_hue", SmartHome, 3.0},
	{"_sftp-ssh", Computer, 2.0},
	{"_ssh", Computer, 2.0},
	{"_smb", Computer, 2.0},
	{"_afpovertcp", Computer, 2.0},
	{"_workstation", Computer, 2.0},
	{"_rfb", Computer, 1.5},
	{"_companion-link", Computer, 1.0},
	{"_nvstream", Console, 2.5},
}

var bleServiceRules = []struct {
	needle string
	cat    Category
	weight float64
	label  string
}{
	{"180d", Wearable, 2.5, "heart rate service"},
	{"1814", Wearable, 2.0, "running speed service"},
	{"1816", Wearable, 2.0, "cycling cadence service"},
	{"1812", Peripheral, 2.5, "human interface service"},
	{"fd6f", Phone, 2.0, "exposure notification"},
	{"feaa", Beacon, 2.5, "Eddystone beacon"},
	{"fe2c", Headphones, 1.5, "Fast Pair"},
	{"110b", Headphones, 2.0, "audio sink"},
	{"1809", SmartHome, 1.5, "thermometer service"},
}

var vendorRules = []keywordRule{
	{"sonos", Speaker, 2.5},
	{"bose", Speaker, 2.0},
	{"jbl", Speaker, 2.0},
	{"harman", Speaker, 2.0},
	{"amazon", Speaker, 1.2},
	{"roku", TV, 2.5},
	{"vizio", TV, 2.5},
	{"hewlett", Printer, 2.0},
	{"canon", Printer, 2.0},
	{"epson", Printer, 2.0},
	{"brother", Printer, 2.0},
	{"lexmark", Printer, 2.0},
	{"xerox", Printer, 2.0},
	{"fitbit", Wearable, 2.5},
	{"garmin", Wearable, 2.5},
	{"huami", Wearable, 2.0},
	{"tile", Tracker, 2.5},
	{"arlo", Camera, 2.5},
	{"wyze", Camera, 2.5},
	{"hikvision", Camera, 2.5},
	{"axis communications", Camera, 2.5},
	{"ecobee", SmartHome, 2.5},
	{"signify", SmartHome, 2.5},
	{"lifx", SmartHome, 2.5},
	{"shelly", SmartHome, 2.5},
	{"nest", SmartHome, 1.5},
	{"espressif", SmartHome, 1.0},
	{"nintendo", Console, 2.5},
	{"sony interactive", Console, 2.5},
	{"logitech", Peripheral, 1.5},
	{"tesla", Vehicle, 2.5},
	{"cisco", Router, 1.5},
	{"netgear", Router, 1.5},
	{"tp-link", Router, 1.5},
	{"ubiquiti", Router, 1.5},
	{"aruba", Router, 1.5},
	{"ruckus", Router, 1.5},
	{"mikrotik", Router, 1.5},
	{"d-link", Router, 1.5},
	{"linksys", Router, 1.5},
	{"raspberry", Computer, 1.5},
}

var nameRules = []keywordRule{
	{"chromecast", TV, 2.5},
	{"firestick", TV, 2.5},
	{"fire tv", TV, 2.5},
	{"bravia", TV, 2.0},
	{"shield", TV, 1.5},
	{" tv", TV, 1.5},
	{"tv ", TV, 1.5},
	{"soundbar", Speaker, 2.5},
	{"speaker", Speaker, 2.0},
	{"homepod", Speaker, 2.5},
	{"echo", Speaker, 2.0},
	{"alexa", Speaker, 2.0},
	{"airpods", Headphones, 3.0},
	{"buds", Headphones, 2.5},
	{"headphone", Headphones, 2.5},
	{"headset", Headphones, 2.0},
	{"beats", Headphones, 2.0},
	{"quietcomfort", Headphones, 2.5},
	{"printer", Printer, 3.0},
	{"laserjet", Printer, 3.0},
	{"officejet", Printer, 3.0},
	{"pixma", Printer, 3.0},
	{"ecotank", Printer, 3.0},
	{"workforce", Printer, 2.0},
	{"watch", Wearable, 2.5},
	{"whoop", Wearable, 2.5},
	{"iphone", Phone, 3.0},
	{"pixel", Phone, 2.0},
	{"galaxy", Phone, 1.5},
	{"oneplus", Phone, 2.0},
	{"phone", Phone, 1.5},
	{"macbook", Computer, 3.0},
	{"imac", Computer, 3.0},
	{"laptop", Computer, 2.5},
	{"thinkpad", Computer, 2.5},
	{"desktop", Computer, 2.0},
	{"camera", Camera, 2.5},
	{"doorbell", Camera, 2.5},
	{"thermostat", SmartHome, 2.5},
	{"bulb", SmartHome, 2.5},
	{"lamp", SmartHome, 2.0},
	{"plug", SmartHome, 2.0},
	{"lock", SmartHome, 2.0},
	{"sensor", SmartHome, 1.5},
	{"playstation", Console, 3.0},
	{"xbox", Console, 3.0},
	{"nintendo", Console, 3.0},
	{"airtag", Tracker, 3.0},
	{"tracker", Tracker, 2.0},
	{"keyboard", Peripheral, 2.5},
	{"mouse", Peripheral, 2.5},
	{"router", Router, 2.0},
	{"gateway", Router, 1.5},
}
