package app

// 视频生成遗留手写路径；已有官方插件的接口类型走声明式协议。

import (
	"context"
	"errors"
	"strings"

	"infinite-canvas/backend/internal/model"
	"infinite-canvas/backend/internal/modelcatalog"
)

func (s *Service) validateResolvedVideoCapability(input *canvasGenerationInput) error {
	return s.validateResolvedVideoCapabilityContext(context.Background(), input)
}

func (s *Service) validateResolvedVideoCapabilityContext(ctx context.Context, input *canvasGenerationInput) error {
	if err := s.resolveVideoCapability(ctx, input); err != nil {
		return err
	}
	if input.VideoCapability == nil {
		return nil
	}
	return validateVideoTask(input.VideoCapability, *input)
}

func (s *Service) resolveVideoCapability(ctx context.Context, input *canvasGenerationInput) error {
	channelID := strings.TrimSpace(input.Config.ChannelID)
	if channelID == "" {
		profile := input.Config.CapabilityConfig
		if profile == nil || profile.Video == nil {
			seedance2 := modelcatalog.IsSeedance2Family(input.Config.InterfaceType, input.Config.Model)
			if input.Config.InterfaceType != string(model.ChannelInterfaceAgnesVideo) && !seedance2 {
				return nil
			}
			profile = DefaultModelCapabilityConfigForModel(input.Config.InterfaceType, input.Config.Model)
		}
		normalized, err := NormalizeModelCapabilityConfigForModel("video", input.Config.InterfaceType, input.Config.Model, profile)
		if err != nil || normalized == nil || normalized.Video == nil {
			return errors.New("当前视频模型能力参数无效")
		}
		input.Config.CapabilityConfig = normalized
		input.VideoCapability = normalized.Video
		applyFixedVideoResolution(input, normalized.Video)
		return nil
	}
	item, err := s.repo.ChannelModelByKey(channelID, providerChannelModelKey(input.Config))
	if err != nil {
		return errors.New("当前系统渠道模型未配置或已停用")
	}
	profile, err := DecodeModelCapabilityConfig(item.CapabilityConfigJSON)
	if err != nil || profile == nil || profile.Video == nil {
		return errors.New("当前视频模型尚未配置能力参数")
	}
	normalized, err := NormalizeModelCapabilityConfigForModel("video", string(item.Protocol), firstNonEmpty(item.ProviderModelKey, item.ModelKey), profile)
	if err != nil || normalized == nil || normalized.Video == nil {
		return errors.New("当前视频模型能力参数无效")
	}
	input.VideoCapability = normalized.Video
	applyFixedVideoResolution(input, normalized.Video)
	return nil
}
