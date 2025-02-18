package service

import (
	"context"
	"encoding/base64"

	"github.com/ujjwalsittu/aadhaar-paperless-offline-ekyc-apis/pkg/aadhaarapi"
	api "github.com/ujjwalsittu/aadhaar-paperless-offline-ekyc-apis/pkg/api/v1"
	"go.uber.org/zap"
)

func (s AadhaarService) getSession(ctx context.Context, reqSessionId string) (sessionId string, status *api.ResponseStatus) {
	fn := "getSession"

	sessionId, err := s.aadhaarCacheStore.GetSession(reqSessionId)
	if err != nil {
		s.log.Error(fn, zap.Any("reqSessionId", reqSessionId), zap.Error(err))
		reason := ApiUnknownError
		if s.aadhaarCacheStore.IsNotFoundError(err) {
			reason = InvalidSessionIdError
		}
		status = mapToStatus(ctx, reason, "")
	}
	return
}

func (s AadhaarService) GetCaptcha(ctx context.Context, req *api.GetCaptchaRequest) (res *api.GetCaptchaResponse, err error) {

	fn := "GetCaptcha"

	var captchaImg []byte
	var sessionCookie string
	for i := 0; i <= 3; i++ {
		captchaImg, sessionCookie, err = aadhaarapi.GetCaptcha()
		if err != nil {
			if aadhaarapi.IsRetryableError(err) {
				s.log.Info(fn, zap.NamedError("retrying_error", err))
				continue
			}
			break
		} else {
			s.log.Info(fn, zap.String("info", "captcha image fetch success"))
			break
		}
	}

	if err != nil {
		s.log.Error(fn, zap.Any("req", req), zap.Error(err))
		return &api.GetCaptchaResponse{
			Status: mapToStatus(ctx, ApiUnknownError, ""),
		}, nil
	}
	hash, err := s.aadhaarCacheStore.SaveSession(sessionCookie)
	if err != nil {
		s.log.Error(fn, zap.Any("req", req), zap.Error(err))
		return &api.GetCaptchaResponse{
			Status: mapToStatus(ctx, ApiUnknownError, ""),
		}, nil
	}
	return &api.GetCaptchaResponse{
		Status: &api.ResponseStatus{
			Code:    ApiSuccessCode,
			Message: "Captcha fetched successfully.",
		},
		Data: &api.GetCaptchaResponse_Data{
			SessionId:    hash,
			CaptchaImage: base64.StdEncoding.EncodeToString(captchaImg),
		},
	}, nil
}

func (s AadhaarService) VerifyCaptcha(ctx context.Context, req *api.VerifyCaptchaRequest) (res *api.VerifyCaptchaResponse, err error) {
	fn := "VerifyCaptcha"
	if err = req.Validate(); err != nil {
		s.log.Error(fn, zap.Any("req", req), zap.Error(err))
		if err, ok := err.(api.VerifyCaptchaRequestValidationError); ok {
			if status := validationErrToStaus(ctx, err); status != nil {
				return &api.VerifyCaptchaResponse{
					Status: status,
				}, nil
			}
		}
		return
	}
	sessionId, status := s.getSession(ctx, req.SessionId)
	if status != nil {
		return &api.VerifyCaptchaResponse{
			Status: status,
		}, nil
	}
	result, err := aadhaarapi.VerifyCaptcha(aadhaarapi.VerifyCaptchaOpt{
		SessionId:    sessionId,
		UidNo:        req.UidNo,
		SecurityCode: req.SecurityCode,
	})
	if err != nil {
		s.log.Error(fn, zap.Any("req", req), zap.Error(err))
		return &api.VerifyCaptchaResponse{
			Status: mapAadhaarErrToStatus(ctx, err),
		}, nil
	}
	return &api.VerifyCaptchaResponse{
		Status: &api.ResponseStatus{
			Code:    ApiSuccessCode,
			Message: result.Msg,
		},
	}, nil
}

func (s AadhaarService) VerifyOtpAndGetAadhaar(ctx context.Context, req *api.VerifyOtpAndGetAadhaarRequest) (res *api.VerifyOtpAndGetAadhaarResponse, err error) {
	fn := "VerifyOtpAndGetAadhaar"
	if err = req.Validate(); err != nil {
		s.log.Error(fn, zap.Any("req", req), zap.Error(err))
		if err, ok := err.(api.VerifyOtpAndGetAadhaarRequestValidationError); ok {
			if status := validationErrToStaus(ctx, err); status != nil {
				return &api.VerifyOtpAndGetAadhaarResponse{
					Status: status,
				}, nil
			}
		}
		return
	}
	sessionId, status := s.getSession(ctx, req.SessionId)
	if status != nil {
		return &api.VerifyOtpAndGetAadhaarResponse{
			Status: status,
		}, nil
	}

	if res, err := s.fetchAadhaarResFromCache(req, sessionId); err != nil {
		if !s.aadhaarCacheStore.IsNotFoundError(err) {
			s.log.Error(fn, zap.Any("req", req), zap.Error(err))
		}
	} else {
		return res, nil
	}

	aadhaarRes, err := aadhaarapi.VerifyOTPAndGetAadhaar(aadhaarapi.VerifyOTPAndGetAadhaarOpt{
		SessionId: sessionId,
		OTP:       req.Otp,
		ZipCode:   req.ZipCode,
	})
	if err != nil {
		s.log.Error(fn, zap.Bool("aadhaarapi.IsRedownloadError(err)", aadhaarapi.IsRedownloadError(err)),
			zap.Bool("aadhaarapi.IsSessionExpired(err)", aadhaarapi.IsSessionExpired(err)), zap.Any("req", req), zap.Error(err))
		return &api.VerifyOtpAndGetAadhaarResponse{
			Status: mapAadhaarErrToStatus(ctx, err),
		}, nil
	}
	{
		// save aadhaarRes to cache
		if err := s.aadhaarCacheStore.SaveData(sessionId, aadhaarRes); err != nil {
			s.log.Error(fn, zap.NamedError("cache_save_error", err))
		}
	}

	return &api.VerifyOtpAndGetAadhaarResponse{
		Status: &api.ResponseStatus{
			Code: ApiSuccessCode,
		},
		Data: s.buildDataFromAadhaarRes(req, aadhaarRes),
	}, nil
}

func (s AadhaarService) buildDataFromAadhaarRes(
	req *api.VerifyOtpAndGetAadhaarRequest,
	aadhaarRes aadhaarapi.VerifyOTPAndGetAadhaarResult) *api.VerifyOtpAndGetAadhaarResponse_Data {
	data := &api.VerifyOtpAndGetAadhaarResponse_Data{
		Details: &api.AadhaarDetails{
			Poi: &api.AadhaarDetails_Poi{
				Dob:        aadhaarRes.Details.UidData.Poi.Dob,
				EmailHash:  aadhaarRes.Details.UidData.Poi.EmailHash,
				Gender:     aadhaarRes.Details.UidData.Poi.Gender,
				MobileHash: aadhaarRes.Details.UidData.Poi.MobileHash,
				Name:       aadhaarRes.Details.UidData.Poi.Name,
			},
			Poa: &api.AadhaarDetails_Poa{
				Careof:     aadhaarRes.Details.UidData.Poa.CareOf,
				Country:    aadhaarRes.Details.UidData.Poa.Country,
				Dist:       aadhaarRes.Details.UidData.Poa.District,
				House:      aadhaarRes.Details.UidData.Poa.House,
				Landmark:   aadhaarRes.Details.UidData.Poa.Landmark,
				Locality:   aadhaarRes.Details.UidData.Poa.Locality,
				Pincode:    aadhaarRes.Details.UidData.Poa.Pincode,
				Postoffice: aadhaarRes.Details.UidData.Poa.Postoffice,
				State:      aadhaarRes.Details.UidData.Poa.State,
				Street:     aadhaarRes.Details.UidData.Poa.Street,
				Subdist:    aadhaarRes.Details.UidData.Poa.Subdist,
				Vtc:        aadhaarRes.Details.UidData.Poa.Vtc,
			},
			Photo: aadhaarRes.Details.UidData.Photo,
		},
	}
	if req.IncludeXmlFile {
		data.XmlFile = base64.StdEncoding.EncodeToString(aadhaarRes.XmlFile)
	}
	if req.IncludeZipFile {
		data.ZipFile = base64.StdEncoding.EncodeToString(aadhaarRes.ZipFile)
	}
	data.XmlSignatureValidated = aadhaarRes.XmlSignatureValidated
	return data
}

func (s AadhaarService) fetchAadhaarResFromCache(
	req *api.VerifyOtpAndGetAadhaarRequest,
	sessionId string,
) (*api.VerifyOtpAndGetAadhaarResponse, error) {
	fn := "fetchAadhaarDetailsFromCache"
	aadhaarRes := aadhaarapi.VerifyOTPAndGetAadhaarResult{}
	err := s.aadhaarCacheStore.GetData(sessionId, &aadhaarRes)
	if err != nil {
		s.log.Error(fn, zap.Any("sessionId", sessionId), zap.Error(err))
		return nil, err
	}

	s.log.Info(fn, zap.String("info", "data serving from cache"))
	return &api.VerifyOtpAndGetAadhaarResponse{
		Status: &api.ResponseStatus{
			Code: ApiSuccessCode,
		},
		Data: s.buildDataFromAadhaarRes(req, aadhaarRes),
	}, nil
}

func (s AadhaarService) VerifyAadhaarNumber(ctx context.Context, req *api.VerifyAadhaarNumberRequest) (res *api.VerifyAadhaarNumberResponse, err error) {
	fn := "VerifyAadhaarNumber"
	if err = req.Validate(); err != nil {
		s.log.Error(fn, zap.Any("req", req), zap.Error(err))
		if err, ok := err.(api.VerifyAadhaarNumberRequestValidationError); ok {
			if status := validationErrToStaus(ctx, err); status != nil {
				return &api.VerifyAadhaarNumberResponse{
					Status: status,
				}, nil
			}
		}
		return
	}
	sessionId, status := s.getSession(ctx, req.SessionId)
	if status != nil {
		return &api.VerifyAadhaarNumberResponse{
			Status: status,
		}, nil
	}
	result, err := aadhaarapi.VerifyAadhaarNumber(aadhaarapi.VerifyCaptchaOpt{
		SessionId:    sessionId,
		UidNo:        req.UidNo,
		SecurityCode: req.SecurityCode,
	})
	if err != nil {
		s.log.Error(fn, zap.Any("req", req), zap.Error(err))
		return &api.VerifyAadhaarNumberResponse{
			Status: mapAadhaarErrToStatus(ctx, err),
		}, nil
	}
	return &api.VerifyAadhaarNumberResponse{
		Status: &api.ResponseStatus{
			Code: ApiSuccessCode,
		},
		Data: &api.VerifyAadhaarNumberResponse_Data{
			Details:      result.Details,
			AgeBand:      result.AgeBand,
			State:        result.State,
			Gender:       result.Gender,
			MobileNumber: result.MobileNumber,
			Verified:     result.IsVerified,
		},
	}, nil
}
