package workflow

import "errors"

var (
	// ErrInvalidTransition 当前状态无匹配的 Action。
	ErrInvalidTransition = errors.New("invalid transition: no matching action")

	// ErrForbidden Guard 检查未通过。
	ErrForbidden = errors.New("forbidden: guard check failed")

	// ErrConditionNotMet Condition 评估未通过。
	ErrConditionNotMet = errors.New("condition not met")

	// ErrStateNotFound 当前状态不存在。
	ErrStateNotFound = errors.New("state not found")

	// ErrStateAlreadyExists 状态名称已注册。
	ErrStateAlreadyExists = errors.New("state already exists")
)
