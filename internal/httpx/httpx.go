// Package httpx 统一响应包裹与错误传递。
package httpx

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
)

type Envelope struct {
	Code    string `json:"code"`           // "ok" 或错误码
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// Error 携带 HTTP 状态与错误码，模块内返回、handler 统一输出。
type Error struct {
	Status int    `json:"-"`
	Code   string `json:"-"`
	Msg    string `json:"-"`
}

func (e *Error) Error() string { return e.Msg }

func NewError(status int, code, msg string) *Error {
	return &Error{Status: status, Code: code, Msg: msg}
}

func BadRequest(msg string) *Error   { return NewError(http.StatusBadRequest, "bad_request", msg) }
func NotFound(msg string) *Error     { return NewError(http.StatusNotFound, "not_found", msg) }
func Unprocessable(msg string) *Error {
	return NewError(http.StatusUnprocessableEntity, "unprocessable", msg)
}

func OK(c *gin.Context, data any) {
	c.JSON(http.StatusOK, Envelope{Code: "ok", Message: "ok", Data: data})
}

func Err(c *gin.Context, status int, code, msg string) {
	c.AbortWithStatusJSON(status, Envelope{Code: code, Message: msg})
}

// Respond 把模块返回的 err 映射为响应；非 *Error 一律 500。
func Respond(c *gin.Context, err error) {
	var he *Error
	if errors.As(err, &he) {
		Err(c, he.Status, he.Code, he.Msg)
		return
	}
	Err(c, http.StatusInternalServerError, "internal", "服务器内部错误")
}
