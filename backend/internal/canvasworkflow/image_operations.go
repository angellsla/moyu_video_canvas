package canvasworkflow

func ImageWorkflowOperations() []Operation {
	out := []Operation{
		{ID: "image.select", Version: 1, Name: "选择图片结果", OutputType: "image", Inputs: []InputPort{{"images", "候选图片", "image", true, true}}},
		{ID: "image.describe", Version: 1, Name: "反推提示词", OutputType: "text", Inputs: []InputPort{{"image", "参考图片", "image", true, false}}, Fields: []Field{{Key: "modelOption", Label: "识图模型", Type: "select"}, {Key: "prompt", Label: "反推要求", Type: "textarea"}}},
	}
	return out
}
