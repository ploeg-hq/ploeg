package shiftengine

import "fmt"

// workItemLink fills the deployment's Work Item template (ReportLinks.WorkItem,
// PLOEG_REPORT_WORK_ITEM_URL) for one Work Item. It is "" when no template is
// configured or the template needs a value this call does not have, the same
// rule the usage report applies.
func workItemLink(links ReportLinks, workItemID int64) string {
	if workItemID <= 0 {
		return ""
	}
	href, ok := fillLink(links.WorkItem, map[string]string{"{id}": fmt.Sprint(workItemID)})
	if !ok {
		return ""
	}
	return href
}

// startedMessage is what the tracker item is told when a Shift opens on it.
// Its one job is to carry the link a deployment configured for following a
// Work Item; without a link the status write says enough, and false means
// nothing is posted. Pure, so the wording is table-testable.
func startedMessage(team, link string) (string, bool) {
	if link == "" {
		return "", false
	}
	return fmt.Sprintf("Ploeg started working this item with Team %s.\n\n**Follow it:** %s\n", team, link), true
}
