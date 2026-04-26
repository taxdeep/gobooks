// 遵循project_guide.md
package services

// reportableJournalEntryWhere is the shared SQL predicate for ordinary
// accounting reports. Reversal pairs are audit facts, not day-to-day report
// activity, so default reports hide both the reversal JE and the original JE it
// cancels. Audit views can query journal_entries / audit_logs directly.
const reportableJournalEntryWhere = `
je.status = 'posted'
AND je.source_type <> 'reversal'
AND NOT EXISTS (
	SELECT 1
	FROM journal_entries rev
	WHERE rev.company_id = je.company_id
	  AND rev.reversed_from_id = je.id
	  AND rev.status = 'posted'
	  AND rev.source_type = 'reversal'
)`
