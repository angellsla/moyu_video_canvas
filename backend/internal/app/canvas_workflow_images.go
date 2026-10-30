package app

import (
	cw "infinite-canvas/backend/internal/canvasworkflow"
	"infinite-canvas/backend/internal/modelcatalog"
)

func workflowDescribeModels(snapshot map[string]any) []modelcatalog.LocalChannelModel {
	return cw.DescribeModels(snapshot)
}

func (s *Service) workflowDescribeRequest(n cw.Node, resource, operation string) (CreateTaskRequest, error) {
	request, err := cw.DescribeRequest(s.compileHost(), n, resource, operation)
	return createTaskRequestFromRecipe(request), err
}
