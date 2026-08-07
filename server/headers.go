package server

import "net/http"

// AgentTokenHeader is the custom header used to pass the agent token.
// Using a header instead of URL query parameter avoids token leakage in access logs.
const AgentTokenHeader = "X-Agent-Token"

// authHeaders returns HTTP headers for authenticated requests to the server.
func authHeaders() http.Header {
	return http.Header{
		AgentTokenHeader: {flags.Token},
	}
}
