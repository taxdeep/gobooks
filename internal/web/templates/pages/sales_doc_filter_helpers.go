package pages

func salesDocFilterInputClass() string {
	return "mt-2 block w-full rounded-md border border-border-input bg-surface px-3 py-2 text-body text-text outline-none focus:ring-2 focus:ring-primary-focus"
}

func salesDocFilterButtonClass(primary bool) string {
	base := "inline-flex items-center rounded-md px-4 py-2 text-body font-semibold focus:outline-none focus-visible:ring-2 focus-visible:ring-primary-focus"
	if primary {
		return base + " bg-primary text-onPrimary hover:bg-primary-hover"
	}
	return base + " border border-border-input text-text-muted3 hover:bg-background hover:text-text"
}
