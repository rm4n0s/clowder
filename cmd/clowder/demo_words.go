package main

// The vocabularies of the large demo (SPEC §10). They are read-only: the
// generator picks from them with a seeded random source, so a given scale
// always makes the same data.

// largeFirstNames and largeLastNames make the names of the users.
var largeFirstNames = []string{
	"ada", "ben", "chloe", "dan", "eva", "finn", "gia", "hugo", "ivy", "jack",
	"kira", "leo", "mia", "noah", "olga", "pete", "quinn", "rosa", "sam", "tina",
	"uma", "vic", "wendy", "xavi", "yara", "zane", "amir", "bea", "cyrus", "dina",
	"eli", "faye", "gus", "hana", "igor", "jade", "kofi", "lena", "milo", "nina",
}

var largeLastNames = []string{
	"Adams", "Baker", "Chen", "Diaz", "Evans", "Fox", "Garcia", "Haddad", "Ito", "Jones",
	"Khan", "Lopez", "Meyer", "Novak", "Okafor", "Patel", "Quist", "Rossi", "Silva", "Tanaka",
	"Ueda", "Vega", "Wagner", "Xu", "Young", "Zhou", "Andersson", "Brown", "Costa", "Dubois",
}

// largeTopics name the rooms; a second round of the list gets a number.
var largeTopics = []string{
	"design", "backend", "frontend", "mobile", "infra", "security", "data", "support", "sales", "marketing",
	"finance", "legal", "hiring", "ops", "qa", "docs", "research", "product", "growth", "billing",
	"onboarding", "analytics", "platform", "payments", "search", "editor", "performance", "accessibility", "releases", "incidents",
}

// largeTags are the tags of the large demo, the most used first.
var largeTags = []string{
	"release", "bug", "docs", "backend", "design", "urgent", "frontend", "review", "planning", "help",
	"idea", "infra", "security", "performance", "meeting", "customer", "welcome", "changelog", "testing", "refactor",
	"deploy", "feedback", "roadmap", "ux", "api", "mobile", "data", "hiring", "retro", "incident",
	"billing", "onboarding", "search", "editor", "legal", "finance", "marketing", "support", "party", "coffee",
	"question", "decision", "proposal", "cleanup", "migration", "metrics", "accessibility", "demo",
}

// largeWords is the vocabulary of the bodies, the most common first.
var largeWords = []string{
	"the", "we", "release", "team", "review", "design", "update", "deploy", "server", "bug",
	"customer", "plan", "meeting", "report", "feature", "test", "build", "fix", "docs", "issue",
	"schedule", "feedback", "launch", "budget", "ticket", "branch", "merge", "deadline", "priority", "owner",
	"backlog", "sprint", "demo", "metric", "cluster", "migration", "database", "query", "cache", "latency",
	"checkout", "invoice", "contract", "hiring", "interview", "offer", "roadmap", "proposal", "decision", "risk",
	"mockup", "prototype", "layout", "color", "font", "icon", "button", "form", "page", "menu",
	"search", "filter", "export", "import", "backup", "restore", "upgrade", "rollback", "monitor", "alert",
	"incident", "outage", "postmortem", "runbook", "oncall", "pager", "dashboard", "graph", "log", "trace",
	"password", "token", "session", "login", "audit", "policy", "access", "review", "approval", "signature",
	"mobile", "browser", "tablet", "keyboard", "shortcut", "dark", "light", "theme", "language", "translation",
	"coffee", "lunch", "party", "holiday", "travel", "office", "remote", "standup", "retro", "kickoff",
	"thanks", "please", "soon", "today", "tomorrow", "friday", "monday", "blocked", "done", "ready",
}

// largeVerbs and largeNouns make the titles of the tasks.
var largeVerbs = []string{
	"Fix", "Write", "Review", "Update", "Ship", "Test", "Plan", "Document", "Refactor", "Investigate",
	"Draft", "Schedule", "Prepare", "Clean up", "Migrate", "Measure",
}

var largeNouns = []string{
	"the login flow", "the release notes", "the onboarding guide", "the billing report", "the search index",
	"the staging server", "the mobile build", "the style guide", "the backup script", "the dashboard",
	"the API docs", "the invoice export", "the hiring plan", "the incident runbook", "the cache layer",
	"the checkout page", "the audit log", "the translation files", "the monitoring alerts", "the demo",
}

// largeEmoji are the reactions of the large demo.
var largeEmoji = []string{
	"\U0001F44D", "❤️", "\U0001F389", "\U0001F680", "\U0001F440", "\U0001F914", "✅", "\U0001F64F",
}

// largeChatLines are what the people of the private conversations say.
var largeChatLines = []string{
	"Did you see the latest build?", "I will have a look after lunch.", "Can we move the meeting to Friday?",
	"Thanks, that helps a lot.", "The release is blocked on the review.", "I pushed a fix, please check.",
	"Who is on call this week?", "Good idea, let us try it.", "I will write it down in the docs.",
	"Coffee in ten minutes?", "The customer replied, I will forward it.", "Done, it is merged.",
}
