package api

import (
	"net/http"

	"github.com/seqyuan/ennote/ennoworker/internal/store"
)

// getRunMCP returns the MCP servers and tools a Run froze before its first
// Provider request.
//
// This is the answer to "why did my MCP tool do nothing in that Run": a frozen
// server records its binding revision, negotiated protocol, catalog digest, and
// whether it was required, so a server that failed or was never reachable is a
// recorded fact rather than an absence.
func (s *Server) getRunMCP(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("runID")
	// The frozen rows carry no foreign key to the Run, so an unknown Run would
	// otherwise answer with an empty surface that reads as "no MCP configured".
	known, err := s.runExists(r, runID)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	if !known {
		writeError(w, r, http.StatusNotFound, "run_not_found", "run not found", false)
		return
	}
	frozen, err := (&store.MCPRunRepo{DB: s.DB}).LoadRunMCP(r.Context(), runID)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	servers := make([]map[string]any, 0, len(frozen.Servers))
	for _, entry := range frozen.Servers {
		tools := make([]map[string]any, 0, len(entry.Tools))
		for _, tool := range entry.Tools {
			tools = append(tools, map[string]any{
				"remoteName":   tool.RemoteName,
				"exposedName":  tool.ExposedName,
				"description":  tool.Description,
				"riskClass":    string(tool.RiskClass),
				"sourceKind":   tool.SourceKind,
				"schemaDigest": tool.SchemaDigest,
			})
		}
		servers = append(servers, map[string]any{
			"id":                   entry.Server.ID,
			"bindingId":            entry.Server.BindingID,
			"bindingRevision":      entry.Server.BindingRevision,
			"profileVersionId":     entry.Server.ProfileVersionID,
			"configDigest":         entry.Server.ConfigDigest,
			"negotiatedProtocol":   entry.Server.NegotiatedProtocol,
			"serverIdentityDigest": entry.Server.ServerIdentityDigest,
			"catalogDigest":        entry.Server.CatalogDigest,
			"required":             entry.Server.Required,
			"unavailableReason":    entry.Server.UnavailableReason,
			"tools":                tools,
		})
	}
	writeData(w, http.StatusOK, map[string]any{"runId": frozen.RunID, "servers": servers})
}

// runExists answers whether a Run row exists in the scoped Session database.
// The table is the authority for run identity, and its absence is the only way
// to tell an unknown Run from a Run that recorded nothing.
func (s *Server) runExists(r *http.Request, runID string) (bool, error) {
	var count int
	if err := s.DB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM agent_runs WHERE id=?`, runID).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}
