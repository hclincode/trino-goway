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
	}

	return in
}
