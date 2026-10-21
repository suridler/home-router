package api

type ContractError struct {
	Message        string
	MaxAssetSizeMB int
	Code           string
	HTTPStatus     int
}

func (failure *ContractError) Error() string { return failure.Code }
