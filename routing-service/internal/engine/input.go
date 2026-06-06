package engine

import (
	pb "github.com/hclincode/trino-goway-routing-service/routerpb"
)

// FromProto converts a proto RouteRequest into a RouteInput.
// All fields are mapped safely: nil sub-messages produce zero values (no panic).
func FromProto(req *pb.RouteRequest) *RouteInput {
	if req == nil {
		return &RouteInput{}
	}

	in := &RouteInput{
		Source:     req.GetTrinoSource(),
		ClientTags: req.GetClientTags(),
		Method:     req.GetMethod(),
		URI:        req.GetRequestUri(),
		RemoteAddr: req.GetRemoteAddr(),
	}

	// ClientTags: GetClientTags returns nil when the field is absent; normalise
	// to an empty (non-nil) slice so provider code can range over it safely.
	if in.ClientTags == nil {
		in.ClientTags = []string{}
	}

	// ParamMap: GetParameterMap returns nil when absent; normalise to empty map.
	pm := req.GetParameterMap()
	if pm == nil {
		in.ParamMap = map[string]string{}
	} else {
		in.ParamMap = pm
	}

	// TrinoRequestUser: may be nil.
	if u := req.GetTrinoRequestUser(); u != nil {
		in.User = u.GetUser()
	}

	// TrinoQueryProperties: may be nil.
	if qp := req.GetTrinoQueryProperties(); qp != nil {
		in.Catalog = qp.GetDefaultCatalog()
		in.Schema = qp.GetDefaultSchema()
		in.Body = qp.GetBody()
		in.IsNew = qp.GetIsNewQuerySubmission()

		// SQL-aware fields (UC-RTG-04): prefer the proto's parsed fields when a
		// (future) SQL-aware gateway populates them — forward-compatible with the
		// gateway gaining its own parser. trino-goway v1 leaves these empty with
		// is_query_parsing_successful=false, so in practice the in-service
		// analyzer fills them later at the pipeline boundary (see WithSQLMeta).
		in.QueryType = qp.GetQueryType()
		in.Catalogs = qp.GetCatalogs()
		in.Schemas = qp.GetSchemas()
		in.CatalogSchemas = qp.GetCatalogSchemas()
		in.Tables = qp.GetTables()
		in.ParseOK = qp.GetIsQueryParsingSuccessful()
		// QueryCategory has no proto field; it is derived in-service alongside the
		// other heuristic fields. Leave empty here.
	}

	// Normalise nil slices to empty (non-nil) so providers can range safely.
	if in.Catalogs == nil {
		in.Catalogs = []string{}
	}
	if in.Schemas == nil {
		in.Schemas = []string{}
	}
	if in.CatalogSchemas == nil {
		in.CatalogSchemas = []string{}
	}
	if in.Tables == nil {
		in.Tables = []string{}
	}

	return in
}

// HasParsedSQL reports whether the RouteInput already carries proto-provided
// parsed SQL fields (a future SQL-aware gateway populated them). When true, the
// in-service analyzer is skipped — the gateway's parse is authoritative.
func (in *RouteInput) HasParsedSQL() bool {
	return in.ParseOK ||
		in.QueryType != "" ||
		len(in.Catalogs) > 0 ||
		len(in.Schemas) > 0 ||
		len(in.CatalogSchemas) > 0 ||
		len(in.Tables) > 0
}
