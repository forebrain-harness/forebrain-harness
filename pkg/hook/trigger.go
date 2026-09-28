package hook

func InteractiveTrigger(trigger string) bool {
	switch trigger {
	case "user":
		return true
	default:
		return false
	}
}
