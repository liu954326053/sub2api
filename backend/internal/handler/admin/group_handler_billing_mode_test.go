package admin

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// groups.billing_mode（openspec add-upstream-rate-sync）创建/更新请求的枚举校验：
// 只放行 group_multiplier | account_upstream，空 = 不变/默认。

func bindGroupRequest[T any](t *testing.T, body string) (T, error) {
	t.Helper()
	var req T
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	err := c.ShouldBindJSON(&req)
	return req, err
}

func TestCreateGroupRequestBillingModeValidation(t *testing.T) {
	t.Run("omitted means default", func(t *testing.T) {
		req, err := bindGroupRequest[CreateGroupRequest](t, `{"name":"g"}`)
		require.NoError(t, err)
		require.Nil(t, req.BillingMode)
	})

	t.Run("group_multiplier accepted", func(t *testing.T) {
		req, err := bindGroupRequest[CreateGroupRequest](t, `{"name":"g","billing_mode":"group_multiplier"}`)
		require.NoError(t, err)
		require.NotNil(t, req.BillingMode)
		require.Equal(t, "group_multiplier", *req.BillingMode)
	})

	t.Run("account_upstream accepted", func(t *testing.T) {
		req, err := bindGroupRequest[CreateGroupRequest](t, `{"name":"g","billing_mode":"account_upstream"}`)
		require.NoError(t, err)
		require.NotNil(t, req.BillingMode)
		require.Equal(t, "account_upstream", *req.BillingMode)
	})

	t.Run("invalid value rejected", func(t *testing.T) {
		_, err := bindGroupRequest[CreateGroupRequest](t, `{"name":"g","billing_mode":"bogus"}`)
		require.Error(t, err)
	})
}

func TestUpdateGroupRequestBillingModeValidation(t *testing.T) {
	t.Run("omitted means unchanged", func(t *testing.T) {
		req, err := bindGroupRequest[UpdateGroupRequest](t, `{}`)
		require.NoError(t, err)
		require.Nil(t, req.BillingMode)
	})

	t.Run("enum values accepted", func(t *testing.T) {
		for _, mode := range []string{"group_multiplier", "account_upstream"} {
			req, err := bindGroupRequest[UpdateGroupRequest](t, `{"billing_mode":"`+mode+`"}`)
			require.NoError(t, err)
			require.NotNil(t, req.BillingMode)
			require.Equal(t, mode, *req.BillingMode)
		}
	})

	t.Run("invalid value rejected", func(t *testing.T) {
		_, err := bindGroupRequest[UpdateGroupRequest](t, `{"billing_mode":"bogus"}`)
		require.Error(t, err)
	})
}
