// Volcengine Ark's plan actions: the request bodies and answer shapes the public
// API reference documents (docs.volcengine.com/docs/ark, Action + Version
// 2024-01-01), kept in one file so a field the vendor moves is one edit and one
// test.
//
// Two absences are deliberate. No action here lists, creates or rewrites an API
// key, because the vendor publishes no such call, and nothing reads a personal
// plan's key back: the console owns that key and the user pastes it. The one key
// an action returns is a team seat's, GetTeamSeatApiKey, which needs
// ArkFullAccess.
package subscription

const (
	volcengineGetPersonalPlan        = "GetPersonalPlan"
	volcengineListArkCodingPlanModel = "ListArkCodingPlanModel"
	volcengineListArkAgentPlanModel  = "ListArkAgentPlanModel"
	volcengineListSeatInfos          = "ListSeatInfos"
	volcengineGetTeamSeatAPIKey      = "GetTeamSeatApiKey"

	// The two values GetPersonalPlan's Plan field takes.
	volcengineAPICodingPlan = "CodingPlan"
	volcengineAPIAgentPlan  = "AgentPlan"
)

// volcenginePersonalPlan is one personal plan's answer: which tier the account
// holds, whether it runs, and its window. A plan the account does not hold
// answers ResourceNotFound (read by volcengineIsNotFound) or no type at all.
type volcenginePersonalPlan struct {
	PlanType  string `json:"PlanType"`
	Status    string `json:"Status"`
	StartTime string `json:"StartTime"`
	EndTime   string `json:"EndTime"`
}

// volcengineSeat is one team seat: its id is what GetTeamSeatApiKey takes, and
// its BizInfo is the only plan label the vendor states, so it is shown verbatim.
type volcengineSeat struct {
	SeatID        string `json:"SeatID"`
	AccountID     string `json:"AccountID"`
	ProjectName   string `json:"ProjectName"`
	BizInfo       string `json:"BizInfo"`
	SeatStatus    int    `json:"SeatStatus"`
	BillingStatus int    `json:"BillingStatus"`
}

// volcengineSeatList is one page of seats, plus the per-plan summary the same
// answer carries.
type volcengineSeatList struct {
	BizSummaries []struct {
		BizInfo     string `json:"BizInfo"`
		ActiveCount int    `json:"ActiveCount"`
		TotalCount  int    `json:"TotalCount"`
	} `json:"BizSummaries"`
	Data []volcengineSeat `json:"Data"`
}

// volcenginePlanRequest is GetPersonalPlan's body for one plan id.
func volcenginePlanRequest(plan string) map[string]string {
	if plan == volcengineAgentPlan {
		return map[string]string{"Plan": volcengineAPIAgentPlan}
	}
	return map[string]string{"Plan": volcengineAPICodingPlan}
}

// volcengineModelAction is the action and body that list one plan's models: the
// Coding Plan has one catalog, and the Agent Plan splits personal from
// enterprise.
func volcengineModelAction(plan string) (string, any) {
	switch plan {
	case volcengineAgentPlan:
		return volcengineListArkAgentPlanModel, map[string]string{"Edition": "personal"}
	case volcengineAgentPlanTeam:
		return volcengineListArkAgentPlanModel, map[string]string{"Edition": "enterprise"}
	default:
		return volcengineListArkCodingPlanModel, map[string]any{}
	}
}
