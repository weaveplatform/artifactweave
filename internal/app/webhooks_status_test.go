package app

func webhookStatusRows() []statusCase {
	const base = "/event/api/v1/subscriptions"
	const member = base + "/{key}"
	const body = `{"key":"status-hook","enabled":true,"event_filter":{"domain":"user","event_types":["locked"],"criteria":{}},"handlers":[{"handler_type":"webhook","url":"https://receiver.example/hook","secret":"private-status-secret"}]}`
	const update = `{"enabled":false,"event_filter":{"domain":"user","event_types":["locked"],"criteria":{}},"handlers":[{"handler_type":"webhook","url":"https://receiver.example/updated"}]}`
	return []statusCase{
		{"GET " + base, base, "admin", "", 200},
		{"GET " + base, base, "user", "", 403},
		{"GET " + base, base, "", "", 401},
		{"POST " + base, base, "admin", body, 201},
		{"POST " + base, base, "admin", body, 400},
		{"POST " + base, base, "user", body, 403},
		{"POST " + base, base, "", body, 401},
		{"GET " + member, base + "/status-hook", "admin", "", 200},
		{"GET " + member, base + "/status-hook", "user", "", 403},
		{"GET " + member, base + "/status-hook", "", "", 401},
		{"GET " + member, base + "/missing-hook", "admin", "", 404},
		{"PUT " + member, base + "/status-hook", "admin", update, 204},
		{"PUT " + member, base + "/status-hook", "admin", `{"enabled":true,"event_filter":{"domain":"user","event_types":["locked"]},"handlers":[]}`, 400},
		{"PUT " + member, base + "/status-hook", "user", update, 403},
		{"PUT " + member, base + "/status-hook", "", update, 401},
		{"PUT " + member, base + "/missing-hook", "admin", update, 404},
		{"DELETE " + member, base + "/status-hook", "user", "", 403},
		{"DELETE " + member, base + "/status-hook", "", "", 401},
		{"DELETE " + member, base + "/status-hook", "admin", "", 204},
		{"DELETE " + member, base + "/missing-hook", "admin", "", 404},
	}
}
