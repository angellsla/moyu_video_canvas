package generation

// 图片生成（OpenAI / Claude / Gemini / Grok 等手写路径）。

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"infinite-canvas/backend/internal/model"
)

// usesSeedreamRequestBody 判断这次出图是否走 Seedream 专用请求体。Seedream 型号自带
// 请求体，能保留图层拆分、图片格式、水印和提示词优化设置，改走通用插件会静默丢掉它们。
// 例外是 Agent Plan 图片协议：它走 /api/plan/v3/images/generations，只有声明式插件知道
// 那个端点，按型号名套用官方 Ark 路径会把请求发错地址。
func usesSeedreamRequestBody(input Input) bool {
	if seedreamFamily(input.Config.Model) == "" {
		return false
	}
	return model.ChannelInterfaceType(input.Config.InterfaceType) != model.ChannelInterfaceVolcengineArkAgentPlanImage
}

func RunImageTask(ctx context.Context, input Input) (map[string]interface{}, error) {
	if usesSeedreamRequestBody(input) {
		return runSeedreamImageTask(ctx, input)
	}
	// mj_imagine keeps the existing Images endpoint; only its JSON body differs.
	if input.Config.InterfaceType == string(model.ChannelInterfaceOpenAIImage) && strings.EqualFold(strings.TrimSpace(input.Config.Model), "mj_imagine") {
		return runMidjourneyImagineImageTask(ctx, input)
	}
	outputCount := 1
	if input.ImageCapability != nil && input.ImageCapability.FixedOutputs > 0 {
		outputCount = input.ImageCapability.FixedOutputs
	}
	if _, ok := DeclarativeProtocolAdapterForContext(ctx, input.Config.InterfaceType); ok {
		return RunDeclarativeProtocolTask(ctx, input)
	}
	if input.Config.InterfaceType == string(model.ChannelInterfaceGrokImage) {
		return RunGrokImageTask(ctx, input)
	}
	if input.Config.InterfaceType == string(model.ChannelInterfaceVolcengineJiMengImage) {
		return RunVolcengineJiMengImageTask(ctx, input)
	}
	if model.IsVolcengineArkImageProtocol(model.ChannelInterfaceType(input.Config.InterfaceType)) {
		return RunVolcengineArkImageTask(ctx, input)
	}
	if input.Config.InterfaceType == string(model.ChannelInterfaceGeminiImage) {
		return RunGeminiImageTask(ctx, input)
	}
	var payload ImageResponse
	if input.Mask != nil {
		// 蒙版编辑是强校验写路径：协议能力不明确时必须失败，不能静默退化为整图重绘。
		if strings.TrimSpace(input.Config.InterfaceType) != string(model.ChannelInterfaceOpenAIImage) {
			return nil, errors.New("当前渠道未声明 OpenAI Images 编辑协议，已拒绝可能忽略蒙版的整图重绘")
		}
		if len(input.ReferenceImages) == 0 {
			return nil, errors.New("蒙版编辑必须提供与蒙版同尺寸的源图片")
		}
	}
	if len(input.ReferenceImages) > 0 || input.Mask != nil {
		body := &bytes.Buffer{}
		writer := multipart.NewWriter(body)
		writeField(writer, "model", input.Config.Model)
		writeField(writer, "prompt", WithSystemPrompt(input.Config, input.Prompt))
		writeField(writer, "n", strconv.Itoa(outputCount))
		if ImageParameterSupported(input.ImageCapability, "response_format") {
			writeField(writer, "response_format", ImageResponseFormatValue(input.ImageCapability))
		}
		if ImageParameterSupported(input.ImageCapability, "output_format") {
			writeField(writer, "output_format", "png")
		}
		if ImageTransparentBackgroundSupported(input.ImageCapability) && input.Config.TransparentBackground == "true" {
			writeField(writer, "background", "transparent")
		}
		if ImageQualitySupported(input.ImageCapability) && input.Config.Quality != "" && !strings.EqualFold(strings.TrimSpace(input.Config.Quality), "auto") {
			writeField(writer, "quality", NormalizeImageQuality(input.Config.Quality))
		}
		if key, value := ImageSizeParameter(input.ImageCapability, input.Config.Size); value != "" {
			writeField(writer, key, value)
		}
		for _, image := range input.ReferenceImages {
			if err := writeMediaPart(writer, "image", image); err != nil {
				return nil, err
			}
		}
		if input.Mask != nil {
			if err := writeMediaPart(writer, "mask", *input.Mask); err != nil {
				return nil, err
			}
		}
		if err := writer.Close(); err != nil {
			return nil, err
		}
		if err := PostForm(ctx, input.Config, "/images/edits", writer.FormDataContentType(), body, &payload); err != nil {
			return nil, err
		}
	} else {
		body := map[string]interface{}{
			"model":  input.Config.Model,
			"prompt": WithSystemPrompt(input.Config, input.Prompt),
			"n":      outputCount,
		}
		if ImageParameterSupported(input.ImageCapability, "response_format") {
			body["response_format"] = ImageResponseFormatValue(input.ImageCapability)
		}
		if ImageParameterSupported(input.ImageCapability, "output_format") {
			body["output_format"] = "png"
		}
		if ImageTransparentBackgroundSupported(input.ImageCapability) && input.Config.TransparentBackground == "true" {
			body["background"] = "transparent"
		}
		if ImageQualitySupported(input.ImageCapability) && input.Config.Quality != "" && !strings.EqualFold(strings.TrimSpace(input.Config.Quality), "auto") {
			body["quality"] = NormalizeImageQuality(input.Config.Quality)
		}
		if key, value := ImageSizeParameter(input.ImageCapability, input.Config.Size); value != "" {
			body[key] = value
		}
		if err := PostJSON(ctx, input.Config, "/images/generations", body, &payload); err != nil {
			return nil, err
		}
	}
	images, err := ImageDataURLs(payload)
	if err != nil {
		return nil, err
	}
	result := map[string]interface{}{"mode": "image", "images": images}
	if payload.Usage != nil {
		result["usage"] = payload.Usage
	}
	return result, nil
}

func runMidjourneyImagineImageTask(ctx context.Context, input Input) (map[string]interface{}, error) {
	prepared, err := prepareMidjourneyImagineInput(ctx, input)
	if err != nil {
		return nil, err
	}
	body := map[string]interface{}{
		"model":  prepared.Config.Model,
		"prompt": prepared.Prompt,
	}
	if len(prepared.ReferenceImages) > 0 {
		images := make([]string, 0, len(prepared.ReferenceImages))
		for _, image := range prepared.ReferenceImages {
			images = append(images, image.DataURL)
		}
		body["base64Array"] = images
	}
	var payload ImageResponse
	if err := PostJSON(ctx, prepared.Config, "/images/generations", body, &payload); err != nil {
		return nil, err
	}
	images, err := ImageDataURLs(payload)
	if err != nil {
		return nil, err
	}
	result := map[string]interface{}{"mode": "image", "images": images}
	if payload.Usage != nil {
		result["usage"] = payload.Usage
	}
	return result, nil
}

func prepareMidjourneyImagineInput(ctx context.Context, input Input) (Input, error) {
	if !strings.EqualFold(strings.TrimSpace(input.Config.Model), "mj_imagine") {
		return Input{}, errors.New("Midjourney Imagine 协议仅支持 mj_imagine")
	}
	if input.Mask != nil || len(input.ReferenceVideos) > 0 || len(input.ReferenceAudios) > 0 {
		return Input{}, errors.New("Midjourney Imagine 只支持提示词和参考图片，不支持蒙版、视频或音频")
	}
	if count := strings.TrimSpace(input.Config.Count); count != "" && count != "1" {
		return Input{}, errors.New("Midjourney Imagine 单次只返回一张四宫格图片")
	}
	if quality := strings.ToLower(strings.TrimSpace(input.Config.Quality)); quality != "" && quality != "auto" && quality != "any" {
		return Input{}, errors.New("Midjourney Imagine 的质量等参数请写入提示词，不使用图片质量选项")
	}
	if ParseBool(input.Config.TransparentBackground, false) {
		return Input{}, errors.New("Midjourney Imagine 不支持透明背景选项")
	}
	prompt := strings.TrimSpace(WithSystemPrompt(input.Config, input.Prompt))
	if prompt == "" {
		return Input{}, errors.New("Midjourney Imagine 提示词不能为空")
	}
	prompt, err := normalizeMidjourneyPrompt(prompt, input.Config.Size, input.Config.MidjourneyParams)
	if err != nil {
		return Input{}, err
	}
	input.Prompt = prompt
	input.Config.SystemPrompt = ""
	for index := range input.ReferenceImages {
		image := &input.ReferenceImages[index]
		reference := ProtocolMediaReference(*image, "image", index)
		data, _, err := ProtocolMediaBytes(ctx, input.Config, reference)
		if err != nil {
			return Input{}, fmt.Errorf("读取 Midjourney 参考图 %d 失败：%w", index+1, err)
		}
		if len(data) == 0 || len(data) > 10*1024*1024 {
			return Input{}, fmt.Errorf("Midjourney 参考图 %d 必须为非空且不超过 10 MiB", index+1)
		}
		mimeType := strings.ToLower(strings.TrimSpace(strings.Split(http.DetectContentType(data), ";")[0]))
		if !strings.HasPrefix(mimeType, "image/") {
			return Input{}, fmt.Errorf("Midjourney 参考图 %d 不是有效图片", index+1)
		}
		image.DataURL = DataURL(mimeType, data)
		image.URL = ""
		image.MimeType = mimeType
		image.Bytes = int64(len(data))
	}
	return input, nil
}

func midjourneyImagineParameterSuffix(params MidjourneyImagineParams) (string, error) {
	version := strings.TrimSpace(params.Version)
	niji := strings.TrimSpace(params.Niji)
	if version != "" && !oneOf(version, "1", "2", "3", "4", "5", "6", "7", "8.1", "8.2") {
		return "", fmt.Errorf("Midjourney 模型版本无效：%s", version)
	}
	if niji != "" && !oneOf(niji, "6", "7") {
		return "", fmt.Errorf("Midjourney Niji 版本无效：%s", niji)
	}
	if niji != "" && version != "" {
		return "", errors.New("Midjourney --niji 与 --v 不能同时使用")
	}
	if params.Stylize != nil && (*params.Stylize < 0 || *params.Stylize > 1000) {
		return "", errors.New("Midjourney 风格化范围为 0 到 1000")
	}
	if params.Chaos != nil && (*params.Chaos < 0 || *params.Chaos > 100) {
		return "", errors.New("Midjourney 混乱度范围为 0 到 100")
	}
	if params.Weird != nil && (*params.Weird < 0 || *params.Weird > 3000) {
		return "", errors.New("Midjourney 怪异度范围为 0 到 3000")
	}
	if params.Exp != nil && (*params.Exp < 0 || *params.Exp > 100) {
		return "", errors.New("Midjourney 实验性参数范围为 0 到 100")
	}
	if params.Repeat != nil && *params.Repeat < 1 {
		return "", errors.New("Midjourney 重复次数必须大于 0")
	}
	if params.Stop != nil && (*params.Stop < 0 || *params.Stop > 100) {
		return "", errors.New("Midjourney 停止百分比范围为 0 到 100")
	}
	if params.ImageWeight != nil && (*params.ImageWeight < 0 || *params.ImageWeight > 3) {
		return "", errors.New("Midjourney 图片权重范围为 0 到 3")
	}
	if params.Sw != nil && (*params.Sw < 0 || *params.Sw > 1000) {
		return "", errors.New("Midjourney 风格参考权重范围为 0 到 1000")
	}
	if params.Ow != nil && (*params.Ow < 0 || *params.Ow > 1000) {
		return "", errors.New("Midjourney 物体权重范围为 0 到 1000")
	}
	if params.Cw != nil && (*params.Cw < 0 || *params.Cw > 100) {
		return "", errors.New("Midjourney 角色权重范围为 0 到 100")
	}
	if strings.TrimSpace(params.Profile) != "" && (params.Sw != nil || strings.TrimSpace(params.Sv) != "") {
		return "", errors.New("Midjourney Moodboard 不能与 --sw 或 --sv 同时使用")
	}
	if params.SDHD != "" && !oneOf(params.SDHD, "sd", "hd") {
		return "", errors.New("Midjourney 输出档位仅支持 SD 或 HD")
	}
	if params.Quality != "" && !oneOf(params.Quality, "1", "2", "4") {
		return "", errors.New("Midjourney 质量仅支持 1、2 或 4")
	}
	if params.SpeedMode != "" && !oneOf(params.SpeedMode, "fast", "relax", "turbo") {
		return "", errors.New("Midjourney 速度模式无效")
	}
	if niji != "" && (params.Draft || params.SDHD != "" || params.Oref != "" || params.Cref != "") {
		return "", errors.New("Midjourney Niji 不支持 V 系列专属输出或参考参数")
	}

	flags := make([]string, 0, 40)
	add := func(flag string, value string) {
		if value = strings.TrimSpace(value); value != "" {
			flags = append(flags, flag, value)
		}
	}
	if niji != "" {
		add("--niji", niji)
	} else {
		add("--v", version)
	}
	addInt := func(flag string, value *int) {
		if value != nil {
			add(flag, strconv.Itoa(*value))
		}
	}
	addInt("--stylize", params.Stylize)
	addInt("--chaos", params.Chaos)
	addInt("--weird", params.Weird)
	addInt("--exp", params.Exp)
	add("--no", params.No)
	if params.Seed != nil {
		add("--seed", strconv.FormatUint(uint64(*params.Seed), 10))
	}
	if params.Raw {
		flags = append(flags, "--style", "raw")
	}
	if params.Tile {
		flags = append(flags, "--tile")
	}
	addInt("--repeat", params.Repeat)
	addInt("--stop", params.Stop)
	version8 := strings.HasPrefix(version, "8.")
	if version8 && params.Quality != "" {
		return "", errors.New("Midjourney V8.x 请使用 SD/HD 输出档位，不支持 --q")
	}
	if !version8 && params.SDHD != "" {
		return "", errors.New("Midjourney SD/HD 输出仅支持 V8.1 或 V8.2")
	}
	if params.SDHD != "" {
		flags = append(flags, "--"+params.SDHD)
	}
	if params.Quality != "" && (niji != "" || (version != "" && version != "6" && version != "7")) {
		return "", errors.New("Midjourney --q 仅支持 V6 或 V7")
	}
	if params.Quality != "" {
		add("--q", params.Quality)
	}
	if params.Draft {
		if version != "" && version != "7" {
			return "", errors.New("Midjourney 草稿模式仅支持 V7")
		}
		flags = append(flags, "--draft")
	}
	if params.SpeedMode == "turbo" && version8 {
		return "", errors.New("Midjourney V8.x 不支持 Turbo 模式")
	}
	if params.SpeedMode != "" {
		flags = append(flags, "--"+params.SpeedMode)
	}
	if params.ImageWeight != nil {
		add("--iw", strconv.FormatFloat(*params.ImageWeight, 'f', -1, 64))
	}
	add("--sref", params.Sref)
	addInt("--sw", params.Sw)
	add("--sv", params.Sv)
	add("--profile", params.Profile)
	if params.Oref != "" && version != "" && version != "7" {
		return "", errors.New("Midjourney --oref 仅支持 V7")
	}
	add("--oref", params.Oref)
	addInt("--ow", params.Ow)
	if params.Cref != "" && version != "" && version != "6" {
		return "", errors.New("Midjourney --cref 仅支持 V6")
	}
	add("--cref", params.Cref)
	addInt("--cw", params.Cw)
	return strings.Join(flags, " "), nil
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func midjourneyAspectRatio(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(strings.ReplaceAll(value, "×", "x")))
	if value == "" || value == "auto" {
		return "", nil
	}
	separator := ":"
	if strings.Contains(value, "x") {
		separator = "x"
	}
	parts := strings.Split(value, separator)
	if len(parts) != 2 {
		return "", fmt.Errorf("Midjourney 画幅比例无效：%s", value)
	}
	width, widthErr := strconv.Atoi(parts[0])
	height, heightErr := strconv.Atoi(parts[1])
	if widthErr != nil || heightErr != nil || width <= 0 || height <= 0 {
		return "", fmt.Errorf("Midjourney 画幅比例无效：%s", value)
	}
	return fmt.Sprintf("%d:%d", width, height), nil
}

func RunGeminiImageTask(ctx context.Context, input Input) (map[string]interface{}, error) {
	if input.Mask != nil {
		return nil, errors.New("Gemini Images 不支持蒙版编辑，请移除蒙版后重试")
	}
	if len(input.ReferenceVideos) > 0 || len(input.ReferenceAudios) > 0 {
		return nil, errors.New("Gemini Images 不支持参考视频或音频")
	}

	parts := make([]GeminiImageContentPart, 0, 1+len(input.ReferenceImages))
	if prompt := strings.TrimSpace(input.Prompt); prompt != "" {
		parts = append(parts, GeminiImageContentPart{Text: prompt})
	}
	for _, image := range input.ReferenceImages {
		raw, mimeType, err := GeminiImageBytes(image)
		if err != nil {
			return nil, fmt.Errorf("读取 Gemini Images 参考图失败：%w", err)
		}
		parts = append(parts, GeminiImageContentPart{InlineData: &GeminiImageInlineData{MIMEType: mimeType, Data: base64.StdEncoding.EncodeToString(raw)}})
	}
	if len(parts) == 0 {
		return nil, errors.New("Gemini Images 请求缺少提示词或参考图")
	}

	body := GeminiImageRequest{
		Contents: []GeminiImageContent{{Role: "user", Parts: parts}},
		GenerationConfig: GeminiImageGenerationConfig{
			ResponseModalities: []string{"TEXT", "IMAGE"},
			ImageConfig:        GeminiImageConfigFor(input.Config),
		},
	}
	if systemPrompt := strings.TrimSpace(input.Config.SystemPrompt); systemPrompt != "" {
		body.SystemInstruction = &GeminiImageContent{Parts: []GeminiImageContentPart{{Text: systemPrompt}}}
	}
	var payload map[string]interface{}
	path := "/models/" + url.PathEscape(input.Config.Model) + ":generateContent"
	if err := PostGeminiJSON(ctx, input.Config, path, body, &payload); err != nil {
		return nil, err
	}
	images, err := GeminiImageDataURLs(payload)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{"mode": "image", "images": images}, nil
}

func GeminiImageConfigFor(config Config) *GeminiImageConfig {
	imageConfig := &GeminiImageConfig{}
	size := strings.TrimSpace(config.Size)
	if size != "" && size != "auto" && strings.Count(size, ":") == 1 {
		imageConfig.AspectRatio = size
	}
	switch strings.ToLower(strings.TrimSpace(config.Quality)) {
	case "low", "1k":
		imageConfig.ImageSize = "1K"
	case "medium", "2k":
		imageConfig.ImageSize = "2K"
	case "high", "4k":
		imageConfig.ImageSize = "4K"
	}
	if imageConfig.AspectRatio == "" && imageConfig.ImageSize == "" {
		return nil
	}
	return imageConfig
}

func GeminiImageBytes(media Media) ([]byte, string, error) {
	raw, mimeType, err := MediaBytes(media)
	if err != nil {
		return nil, "", err
	}
	if len(raw) == 0 {
		return nil, "", errors.New("参考图片数据为空")
	}
	detected := strings.TrimSpace(strings.Split(http.DetectContentType(raw), ";")[0])
	if !strings.HasPrefix(strings.ToLower(mimeType), "image/") {
		if !strings.HasPrefix(strings.ToLower(detected), "image/") {
			return nil, "", fmt.Errorf("参考图片 MIME 类型无效：%s", defaultString(mimeType, detected))
		}
		mimeType = detected
	} else if !strings.HasPrefix(strings.ToLower(detected), "image/") {
		// 以实际字节签名为准，避免错误的 data URL MIME 把非图片内容伪装成图片。
		return nil, "", fmt.Errorf("参考图片内容不是有效图片：%s", defaultString(detected, mimeType))
	}
	return raw, mimeType, nil
}

func GeminiImageDataURLs(payload map[string]interface{}) ([]map[string]string, error) {
	if errorValue, ok := payload["error"].(map[string]interface{}); ok {
		if message := stringField(errorValue, "message"); message != "" {
			return nil, errors.New(message)
		}
	}
	candidates, _ := payload["candidates"].([]interface{})
	images := make([]map[string]string, 0)
	for _, candidateValue := range candidates {
		candidate, _ := candidateValue.(map[string]interface{})
		content, _ := candidate["content"].(map[string]interface{})
		parts, _ := content["parts"].([]interface{})
		for _, partValue := range parts {
			part, _ := partValue.(map[string]interface{})
			inlineData, _ := part["inlineData"].(map[string]interface{})
			if inlineData == nil {
				inlineData, _ = part["inline_data"].(map[string]interface{})
			}
			if inlineData != nil {
				data := strings.TrimSpace(stringField(inlineData, "data"))
				mimeType := firstNonEmpty(stringField(inlineData, "mimeType"), stringField(inlineData, "mime_type"))
				if data == "" {
					continue
				}
				if mimeType == "" {
					mimeType = "image/png"
				}
				if !strings.HasPrefix(strings.ToLower(mimeType), "image/") {
					return nil, fmt.Errorf("Gemini Images 返回了非图片 MIME 类型：%s", mimeType)
				}
				decoded, err := base64.StdEncoding.DecodeString(data)
				if err != nil {
					return nil, fmt.Errorf("Gemini Images 返回的图片数据无效：%w", err)
				}
				images = append(images, map[string]string{"dataUrl": DataURL(mimeType, decoded)})
				continue
			}
			fileData, _ := part["fileData"].(map[string]interface{})
			if fileData != nil {
				if fileURL := firstNonEmpty(stringField(fileData, "fileUri"), stringField(fileData, "file_uri")); fileURL != "" {
					images = append(images, map[string]string{"dataUrl": fileURL})
				}
			}
		}
	}
	if len(images) == 0 {
		return nil, errors.New("Gemini Images 接口没有返回图片")
	}
	return images, nil
}

func RunGrokImageTask(ctx context.Context, input Input) (map[string]interface{}, error) {
	body, path, err := GrokImageRequestBody(input)
	if err != nil {
		return nil, err
	}
	var payload ImageResponse
	if err := PostJSON(ctx, input.Config, path, body, &payload); err != nil {
		return nil, err
	}
	images, err := ImageDataURLs(payload)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{"mode": "image", "images": images}, nil
}

func GrokImageRequestBody(input Input) (GrokImageRequest, string, error) {
	if input.Mask != nil {
		return GrokImageRequest{}, "", errors.New("Grok 图片协议不支持蒙版编辑，请移除蒙版后重试")
	}
	body := GrokImageRequest{
		Model:          input.Config.Model,
		Prompt:         WithSystemPrompt(input.Config, input.Prompt),
		N:              1,
		ResponseFormat: "url",
		// Grok 图片协议用 aspect_ratio 表达画布比例；同时发送 size 会被上游按 OpenAI 枚举校验并拒绝。
		AspectRatio: NormalizeGrokImageAspectRatio(input.Config.Size),
		Resolution:  NormalizeGrokImageResolution(input.Config.Quality),
	}
	if len(input.ReferenceImages) == 0 {
		return body, "/images/generations", nil
	}
	if len(input.ReferenceImages) != 1 {
		return GrokImageRequest{}, "", fmt.Errorf("Grok 图片编辑只支持 1 张参考图，当前连接了 %d 张", len(input.ReferenceImages))
	}
	imageURL, err := GrokImageInputURL(input.ReferenceImages[0])
	if err != nil {
		return GrokImageRequest{}, "", err
	}
	body.Image = &GrokImageInput{URL: imageURL}
	return body, "/images/edits", nil
}

// normalizeGrokImageResolution 把画布 quality（1k/2k/high…）映射为 grok2api / xAI 的 resolution。
func NormalizeGrokImageResolution(quality string) string {
	raw := strings.ToLower(strings.TrimSpace(quality))
	switch raw {
	case "", "auto":
		return ""
	case "1k", "low", "standard":
		return "1k"
	case "2k", "medium", "hd", "high", "4k":
		// xAI Imagine 图片通常最高 2k；超出则夹到 2k，避免上游拒参。
		return "2k"
	default:
		return ""
	}
}

// normalizeGrokImageAspectRatio 把画布 size（如 1280x720 / 9:16）转成 grok2api / xAI 接受的 aspect_ratio。
func NormalizeGrokImageAspectRatio(size string) string {
	raw := strings.ToLower(strings.TrimSpace(strings.ReplaceAll(size, "×", "x")))
	if raw == "" || raw == "auto" {
		return ""
	}
	if strings.Contains(raw, ":") {
		switch raw {
		case "1:1", "3:4", "4:3", "9:16", "16:9", "2:3", "3:2", "9:19.5", "19.5:9", "1:2", "2:1":
			return raw
		}
	}
	parts := strings.Split(raw, "x")
	if len(parts) != 2 {
		return ""
	}
	w, wErr := strconv.Atoi(parts[0])
	h, hErr := strconv.Atoi(parts[1])
	if wErr != nil || hErr != nil || w <= 0 || h <= 0 {
		return ""
	}
	if w == h {
		return "1:1"
	}
	ratio := float64(w) / float64(h)
	switch {
	case w*9 == h*16 || (ratio >= 1.7 && ratio <= 1.8):
		return "16:9"
	case h*9 == w*16 || (ratio > 0 && ratio <= 1.0/1.7 && ratio >= 1.0/1.8):
		return "9:16"
	case w*3 == h*4 || (ratio > 1.2 && ratio < 1.4):
		return "4:3"
	case h*3 == w*4 || (ratio > 0.7 && ratio < 0.85):
		return "3:4"
	// 像素尺寸路径必须显式覆盖 2:3 / 3:2 / 1:2 / 2:1：冒号字符串能直达（见上方 switch），
	// 但像素路径只靠 w>h 兜底会把 768x1152（2:3，ratio 0.667）错标成 9:16、
	// 1152x768（3:2，ratio 1.5）错标成 16:9，xAI 按错比例裁切生成图。
	case w*3 == h*2 || (ratio >= 0.6 && ratio < 0.72):
		return "2:3"
	case w*2 == h*3 || (ratio > 1.35 && ratio < 1.6):
		return "3:2"
	case h == w*2 || (ratio > 0.45 && ratio < 0.55):
		return "1:2"
	case w == h*2 || (ratio > 1.85 && ratio < 2.2):
		return "2:1"
	case w > h:
		return "16:9"
	default:
		return "9:16"
	}
}

func GrokImageInputURL(media Media) (string, error) {
	if IsPublicMediaURL(strings.TrimSpace(media.URL)) {
		return strings.TrimSpace(media.URL), nil
	}
	return openAIImageInputURL(media)
}

const (
	volcengineArkImageMinPixels = 3686400
	volcengineArkImageMaxPixels = 4624220
)

const (
	VolcengineArkImageMinPixels = volcengineArkImageMinPixels
	VolcengineArkImageMaxPixels = volcengineArkImageMaxPixels
)

func RunVolcengineArkImageTask(ctx context.Context, input Input) (map[string]interface{}, error) {
	if input.Mask != nil {
		return nil, errors.New("火山方舟图片协议不支持蒙版编辑，请移除蒙版后重试")
	}
	body, err := VolcengineArkImageBody(input)
	if err != nil {
		return nil, err
	}
	var payload ImageResponse
	if err := PostJSON(ctx, input.Config, "/images/generations", body, &payload); err != nil {
		return nil, err
	}
	images, err := MaterializeImageDataURLs(ctx, input.Config, payload)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{"mode": "image", "images": images}, nil
}

func MaterializeImageDataURLs(ctx context.Context, config Config, payload ImageResponse) ([]map[string]string, error) {
	images, err := ImageDataURLs(payload)
	if err != nil {
		return nil, err
	}
	for _, image := range images {
		value := strings.TrimSpace(image["dataUrl"])
		if strings.HasPrefix(value, "data:image/") {
			continue
		}
		if !IsPublicMediaURL(value) {
			return nil, errors.New("图片接口没有返回可下载的图片")
		}
		// URL results must become bytes before task persistence. Keep provider
		// authentication on same-origin content routes, never on external CDNs.
		data, mimeType, err := GetProviderExternalBinary(WithRequestKind(ctx, "download"), config, value)
		if err != nil {
			return nil, fmt.Errorf("图片结果下载失败：%w", err)
		}
		detected := strings.ToLower(strings.TrimSpace(strings.Split(http.DetectContentType(data), ";")[0]))
		mimeType = strings.ToLower(NormalizedMediaMIMEType(mimeType, data))
		if len(data) == 0 || strings.Contains(detected, "json") || strings.HasPrefix(detected, "text/") || !strings.HasPrefix(mimeType, "image/") {
			return nil, fmt.Errorf("图片结果无效：%s", defaultString(detected, mimeType))
		}
		image["dataUrl"] = DataURL(mimeType, data)
		image["mimeType"] = mimeType
	}
	return images, nil
}

func VolcengineArkImageBody(input Input) (map[string]interface{}, error) {
	body := map[string]interface{}{
		"model":           input.Config.Model,
		"prompt":          WithSystemPrompt(input.Config, input.Prompt),
		"n":               1,
		"response_format": "b64_json",
		"watermark":       false,
	}
	if key, value := ImageSizeParameter(input.ImageCapability, input.Config.Size); value != "" {
		if key == "size" {
			value = NormalizeVolcengineArkImageSize(value)
		}
		body[key] = value
	}
	if len(input.ReferenceImages) == 0 {
		return body, nil
	}
	images := make([]string, 0, len(input.ReferenceImages))
	for _, image := range input.ReferenceImages {
		url, err := openAIImageInputURL(image)
		if err != nil {
			return nil, err
		}
		images = append(images, url)
	}
	if len(images) == 1 {
		body["image"] = images[0]
	} else {
		body["image"] = images
	}
	return body, nil
}

func NormalizeVolcengineArkImageSize(value string) string {
	size := NormalizePixelSize(value)
	parts := strings.Split(strings.ToLower(size), "x")
	if len(parts) != 2 {
		return size
	}
	width, widthErr := strconv.Atoi(parts[0])
	height, heightErr := strconv.Atoi(parts[1])
	if widthErr != nil || heightErr != nil || width <= 0 || height <= 0 {
		return size
	}
	pixels := int64(width) * int64(height)
	if pixels >= volcengineArkImageMinPixels && pixels <= volcengineArkImageMaxPixels {
		return size
	}
	targetPixels := volcengineArkImageMaxPixels
	round := math.Floor
	if pixels < volcengineArkImageMinPixels {
		targetPixels = volcengineArkImageMinPixels
		round = math.Ceil
	}
	scale := math.Sqrt(float64(targetPixels) / float64(pixels))
	width = int(round(float64(width)*scale/2)) * 2
	height = int(round(float64(height)*scale/2)) * 2
	for width > 2 && height > 2 && int64(width)*int64(height) < volcengineArkImageMinPixels {
		if width >= height {
			width += 2
		} else {
			height += 2
		}
	}
	for width > 2 && height > 2 && int64(width)*int64(height) > volcengineArkImageMaxPixels {
		if width >= height {
			width -= 2
		} else {
			height -= 2
		}
	}
	return strconv.Itoa(width) + "x" + strconv.Itoa(height)
}

func ImageDataURLs(payload ImageResponse) ([]map[string]string, error) {
	if len(payload.Data) == 0 {
		return nil, errors.New("接口没有返回图片")
	}
	images := make([]map[string]string, 0, len(payload.Data))
	for _, item := range payload.Data {
		if b64, ok := item["b64_json"].(string); ok && b64 != "" {
			images = append(images, map[string]string{"dataUrl": "data:image/png;base64," + b64})
			continue
		}
		if url, ok := item["url"].(string); ok && url != "" {
			images = append(images, map[string]string{"dataUrl": url})
		}
	}
	if len(images) == 0 {
		return nil, errors.New("接口没有返回可用图片")
	}
	return images, nil
}
