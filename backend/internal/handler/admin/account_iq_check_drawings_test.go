package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestParseIQCheckDrawingIDs(t *testing.T) {
	ids, err := parseIQCheckDrawingIDs(" 3, 1,3,,2 ")
	require.NoError(t, err)
	require.Equal(t, []int64{3, 1, 2}, ids)

	ids, err = parseIQCheckDrawingIDs("")
	require.NoError(t, err)
	require.Empty(t, ids)

	for _, raw := range []string{"1,x", "0", "-4"} {
		_, err = parseIQCheckDrawingIDs(raw)
		require.Error(t, err, raw)
	}

	many := make([]string, iqCheckDrawingsMaxIDs+1)
	for i := range many {
		many[i] = fmt.Sprint(i + 1)
	}
	_, err = parseIQCheckDrawingIDs(strings.Join(many, ","))
	require.ErrorContains(t, err, "at most")
}

func TestListIQCheckDrawingsWithoutServiceReturnsEmptyList(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/admin/accounts/iq-check-drawings?ids=1,2", nil).WithContext(context.Background())

	(&AccountHandler{}).ListIQCheckDrawings(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	var body struct {
		Data []service.IQCheckDrawing `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	require.NotNil(t, body.Data)
	require.Empty(t, body.Data)

	bad := httptest.NewRecorder()
	c, _ = gin.CreateTestContext(bad)
	c.Request = httptest.NewRequest(http.MethodGet, "/admin/accounts/iq-check-drawings?ids=abc", nil)
	(&AccountHandler{}).ListIQCheckDrawings(c)
	require.Equal(t, http.StatusBadRequest, bad.Code)
}
