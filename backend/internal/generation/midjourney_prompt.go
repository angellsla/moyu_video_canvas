package generation

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

type midjourneyOption struct {
	key      string
	flag     string
	value    string
	hasValue bool
}

type midjourneyOptionMatch struct {
	start     int
	end       int
	name      string
	inline    string
	hasInline bool
}

var midjourneyOptionPattern = regexp.MustCompile(`--([a-z][a-z0-9-]*)(?:=([^\s]+))?`)
var midjourneyHorizontalSpacePattern = regexp.MustCompile(`[ \t]{2,}`)

func normalizeMidjourneyPrompt(prompt, size string, params MidjourneyImagineParams) (string, error) {
	body, promptOptions, err := extractMidjourneyPromptOptions(strings.TrimSpace(prompt))
	if err != nil {
		return "", err
	}
	configuredSuffix, err := midjourneyImagineParameterSuffix(params)
	if err != nil {
		return "", err
	}
	_, configuredOptions, err := extractMidjourneyPromptOptions(configuredSuffix)
	if err != nil {
		return "", err
	}

	options := make(map[string]midjourneyOption, len(promptOptions)+len(configuredOptions)+1)
	merge := func(incoming []midjourneyOption) error {
		for _, option := range incoming {
			if current, exists := options[option.key]; exists {
				if current.flag != option.flag || current.value != option.value || current.hasValue != option.hasValue {
					return fmt.Errorf("Midjourney 参数 %s 与 %s 冲突，请只保留一个设置", current.flag, option.flag)
				}
				continue
			}
			options[option.key] = option
		}
		return nil
	}
	if err := merge(promptOptions); err != nil {
		return "", err
	}
	if err := merge(configuredOptions); err != nil {
		return "", err
	}
	if _, exists := options["ar"]; !exists {
		ratio, err := midjourneyAspectRatio(size)
		if err != nil {
			return "", err
		}
		if ratio != "" {
			options["ar"] = midjourneyOption{key: "ar", flag: "--ar", value: ratio, hasValue: true}
		}
	}
	if err := validateMidjourneyOptions(options); err != nil {
		return "", err
	}

	// Keep one stable parameter order and place --no last because it can take a list.
	ordered := make([]string, 0, len(options))
	for _, key := range []string{"ar", "version", "niji", "stylize", "chaos", "weird", "exp", "seed", "style", "tile", "repeat", "stop", "quality", "draft", "speed", "iw", "sref", "sw", "sv", "profile", "oref", "ow", "cref", "cw", "output", "no"} {
		if _, exists := options[key]; exists {
			ordered = append(ordered, key)
		}
	}
	suffix := make([]string, 0, len(ordered)*2)
	for _, key := range ordered {
		option := options[key]
		suffix = append(suffix, option.flag)
		if option.hasValue {
			suffix = append(suffix, option.value)
		}
	}
	if len(suffix) == 0 {
		return strings.TrimSpace(body), nil
	}
	cleanBody := strings.TrimSpace(body)
	if cleanBody == "" {
		return strings.Join(suffix, " "), nil
	}
	return cleanBody + " " + strings.Join(suffix, " "), nil
}

func extractMidjourneyPromptOptions(prompt string) (string, []midjourneyOption, error) {
	matches := midjourneyOptionPattern.FindAllStringSubmatchIndex(prompt, -1)
	markers := make([]midjourneyOptionMatch, 0, len(matches))
	for _, match := range matches {
		start := match[0]
		if start > 0 && !isPromptWhitespace(prompt[start-1]) {
			continue
		}
		marker := midjourneyOptionMatch{start: start, end: match[1], name: strings.ToLower(prompt[match[2]:match[3]])}
		if match[4] >= 0 {
			marker.inline = prompt[match[4]:match[5]]
			marker.hasInline = true
		}
		markers = append(markers, marker)
	}
	if len(markers) == 0 {
		return prompt, nil, nil
	}

	options := make([]midjourneyOption, 0, len(markers))
	spans := make([][2]int, 0, len(markers))
	seen := make(map[string]midjourneyOption, len(markers))
	for index, marker := range markers {
		key, flag, requiresValue, variableValue, known := canonicalMidjourneyOption(marker.name)
		if !known {
			return "", nil, fmt.Errorf("不支持的 Midjourney 参数：--%s", marker.name)
		}
		nextMarker := len(prompt)
		if index+1 < len(markers) {
			nextMarker = markers[index+1].start
		}
		value := strings.TrimSpace(marker.inline)
		hasValue := marker.hasInline
		spanEnd := marker.end
		if marker.name == "raw" {
			if marker.hasInline {
				return "", nil, fmt.Errorf("Midjourney 参数 --raw 不接受值")
			}
			value = "raw"
			hasValue = true
		}
		if marker.hasInline && !requiresValue {
			return "", nil, fmt.Errorf("Midjourney 参数 --%s 不接受值", marker.name)
		}
		if requiresValue && !hasValue {
			valueStart := marker.end
			for valueStart < nextMarker && isPromptWhitespace(prompt[valueStart]) {
				valueStart++
			}
			if valueStart >= nextMarker {
				return "", nil, fmt.Errorf("Midjourney 参数 --%s 缺少值", marker.name)
			}
			if variableValue {
				value = strings.TrimSpace(prompt[valueStart:nextMarker])
				spanEnd = nextMarker
			} else {
				valueEnd := valueStart
				for valueEnd < nextMarker && !isPromptWhitespace(prompt[valueEnd]) {
					valueEnd++
				}
				value = prompt[valueStart:valueEnd]
				spanEnd = valueEnd
			}
			hasValue = true
		}
		option := midjourneyOption{key: key, flag: flag, value: value, hasValue: hasValue}
		if current, exists := seen[key]; exists {
			if current.flag != option.flag || current.value != option.value || current.hasValue != option.hasValue {
				return "", nil, fmt.Errorf("Midjourney 参数 %s 重复且值不同", current.flag)
			}
		} else {
			seen[key] = option
			options = append(options, option)
		}
		spans = append(spans, [2]int{marker.start, spanEnd})
	}

	var body strings.Builder
	position := 0
	for _, span := range spans {
		if span[0] < position {
			continue
		}
		body.WriteString(prompt[position:span[0]])
		position = span[1]
	}
	body.WriteString(prompt[position:])
	cleanBody := midjourneyHorizontalSpacePattern.ReplaceAllString(body.String(), " ")
	return strings.TrimSpace(cleanBody), options, nil
}

func canonicalMidjourneyOption(name string) (key, flag string, requiresValue, variableValue, known bool) {
	aliases := map[string]string{
		"ar": "ar", "aspect": "ar", "v": "version", "version": "version", "niji": "niji",
		"s": "stylize", "stylize": "stylize", "c": "chaos", "chaos": "chaos", "w": "weird", "weird": "weird",
		"exp": "exp", "no": "no", "seed": "seed", "style": "style", "raw": "style", "tile": "tile",
		"r": "repeat", "repeat": "repeat", "stop": "stop", "q": "quality", "quality": "quality",
		"draft": "draft", "fast": "speed", "relax": "speed", "turbo": "speed", "iw": "iw",
		"sref": "sref", "sw": "sw", "sv": "sv", "profile": "profile", "oref": "oref", "ow": "ow",
		"cref": "cref", "cw": "cw", "hd": "output", "sd": "output",
	}
	key, known = aliases[name]
	if !known {
		return "", "", false, false, false
	}
	flag = "--" + key
	switch key {
	case "ar", "version", "niji", "stylize", "chaos", "weird", "exp", "no", "seed", "style", "repeat", "stop", "quality", "iw", "sref", "sw", "sv", "profile", "oref", "ow", "cref", "cw":
		requiresValue = true
	}
	variableValue = key == "no"
	if key == "speed" {
		flag = "--" + name
	}
	if key == "output" {
		flag = "--" + name
	}
	if name == "raw" {
		flag = "--style"
		return key, flag, true, false, true
	}
	if name == "v" || name == "version" {
		flag = "--v"
	} else if name == "s" || name == "stylize" {
		flag = "--stylize"
	} else if name == "c" || name == "chaos" {
		flag = "--chaos"
	} else if name == "w" || name == "weird" {
		flag = "--weird"
	} else if name == "r" || name == "repeat" {
		flag = "--repeat"
	} else if name == "q" || name == "quality" {
		flag = "--q"
	} else if name == "aspect" {
		flag = "--ar"
	}
	if (name == "fast" || name == "relax" || name == "turbo") && !requiresValue {
		key = "speed"
	}
	if key == "speed" {
		return key, "--" + name, false, false, true
	}
	if key == "output" {
		return key, "--" + name, false, false, true
	}
	if key == "style" && name == "style" {
		return key, "--style", true, false, true
	}
	return key, flag, requiresValue, variableValue, known
}

func validateMidjourneyOptions(options map[string]midjourneyOption) error {
	value := func(key string) string { return options[key].value }
	for key, limits := range map[string][2]float64{
		"stylize": {0, 1000}, "chaos": {0, 100}, "weird": {0, 3000}, "exp": {0, 100},
		"repeat": {1, math.MaxInt}, "stop": {0, 100}, "sw": {0, 1000}, "ow": {0, 1000}, "cw": {0, 100},
		"seed": {0, math.MaxUint32},
	} {
		if option, exists := options[key]; exists {
			number, err := strconv.ParseFloat(option.value, 64)
			if err != nil || math.IsNaN(number) || math.IsInf(number, 0) || number < limits[0] || number > limits[1] || number != math.Trunc(number) {
				return fmt.Errorf("Midjourney 参数 %s 的值无效：%s", option.flag, option.value)
			}
		}
	}
	if option, exists := options["iw"]; exists {
		number, err := strconv.ParseFloat(option.value, 64)
		if err != nil || math.IsNaN(number) || math.IsInf(number, 0) || number < 0 || number > 3 {
			return fmt.Errorf("Midjourney 图片权重范围为 0 到 3：%s", option.value)
		}
	}
	if option, exists := options["ar"]; exists {
		if _, err := midjourneyAspectRatio(option.value); err != nil {
			return err
		}
	}
	if option, exists := options["version"]; exists && !oneOf(option.value, "1", "2", "3", "4", "5", "6", "7", "8.1", "8.2") {
		return fmt.Errorf("Midjourney 模型版本无效：%s", option.value)
	}
	if option, exists := options["niji"]; exists && !oneOf(option.value, "6", "7") {
		return fmt.Errorf("Midjourney Niji 版本无效：%s", option.value)
	}
	if _, hasVersion := options["version"]; hasVersion {
		if _, hasNiji := options["niji"]; hasNiji {
			return fmt.Errorf("Midjourney --niji 与 --v 不能同时使用")
		}
	}
	if option, exists := options["quality"]; exists && !oneOf(option.value, "1", "2", "4") {
		return fmt.Errorf("Midjourney 质量仅支持 1、2 或 4")
	}
	if option, exists := options["style"]; exists && !oneOf(strings.ToLower(option.value), "raw", "expressive", "cute", "scenic", "original") {
		return fmt.Errorf("Midjourney --style 值无效：%s", option.value)
	}
	if option, exists := options["no"]; exists && strings.TrimSpace(option.value) == "" {
		return fmt.Errorf("Midjourney --no 缺少排除内容")
	}
	if _, hasHD := options["output"]; hasHD {
		if option, exists := options["output"]; exists && !oneOf(strings.TrimPrefix(option.flag, "--"), "sd", "hd") {
			return fmt.Errorf("Midjourney 输出档位仅支持 SD 或 HD")
		}
	}
	if _, hasSpeed := options["speed"]; hasSpeed {
		if option := options["speed"]; !oneOf(strings.TrimPrefix(option.flag, "--"), "fast", "relax", "turbo") {
			return fmt.Errorf("Midjourney 速度模式无效")
		}
	}
	if _, hasProfile := options["profile"]; hasProfile {
		if _, hasSW := options["sw"]; hasSW {
			return fmt.Errorf("Midjourney Moodboard 不能与 --sw 或 --sv 同时使用")
		}
		if _, hasSV := options["sv"]; hasSV {
			return fmt.Errorf("Midjourney Moodboard 不能与 --sw 或 --sv 同时使用")
		}
	}
	version := value("version")
	if _, exists := options["quality"]; exists && (strings.HasPrefix(version, "8.") || (version != "" && version != "6" && version != "7") || value("niji") != "") {
		return fmt.Errorf("当前 Midjourney 版本不支持 --q")
	}
	if _, exists := options["output"]; exists && version != "8.1" && version != "8.2" {
		return fmt.Errorf("Midjourney SD/HD 输出仅支持 V8.1 或 V8.2")
	}
	if _, exists := options["draft"]; exists && version != "" && version != "7" {
		return fmt.Errorf("Midjourney 草稿模式仅支持 V7")
	}
	if speed, exists := options["speed"]; exists && speed.flag == "--turbo" && strings.HasPrefix(version, "8.") {
		return fmt.Errorf("Midjourney V8.x 不支持 Turbo 模式")
	}
	if _, hasNiji := options["niji"]; hasNiji {
		for _, key := range []string{"draft", "output", "oref", "cref"} {
			if _, exists := options[key]; exists {
				return fmt.Errorf("Midjourney Niji 不支持 %s 参数", options[key].flag)
			}
		}
	}
	if _, exists := options["oref"]; exists && version != "" && version != "7" {
		return fmt.Errorf("Midjourney --oref 仅支持 V7")
	}
	if _, exists := options["cref"]; exists && version != "" && version != "6" {
		return fmt.Errorf("Midjourney --cref 仅支持 V6")
	}
	return nil
}

func isPromptWhitespace(value byte) bool {
	return value == ' ' || value == '\t' || value == '\n' || value == '\r'
}
