package service

import (
	"errors"

	leonardo "github.com/Wei-Shaw/sub2api/internal/pkg/leonardo"
)

// 面向调用方的固定文案：不回显连接错误、解析出的 IP 等内部细节。
const (
	leonardoInputImageBlockedUserMessage = "input image url must be a public https domain on port 443 without redirects"
	leonardoInputImageFetchUserMessage   = "failed to fetch input image"
)

// leonardoInputImageRequestError 把取图错误包装成 Leonardo 终态错误。
func leonardoInputImageRequestError(err error) *leonardo.RequestError {
	reqErr := leonardo.NewRequestError(err.Error())
	switch {
	case errors.Is(err, errInputImageBlocked):
		reqErr.UserMessage = leonardoInputImageBlockedUserMessage
	case errors.Is(err, errInputImageFetchFailed):
		reqErr.UserMessage = leonardoInputImageFetchUserMessage
	}
	return reqErr
}
