package admin

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// iqCheckDrawingsMaxIDs bounds one request; the account list asks per page.
const iqCheckDrawingsMaxIDs = 200

func (h *AccountHandler) SetIQCheckService(iqCheck *service.IQCheckService) {
	h.iqCheck = iqCheck
}

// ListIQCheckDrawings returns the latest IQ check drawing of each requested
// account (GET /admin/accounts/iq-check-drawings?ids=1,2,3).
func (h *AccountHandler) ListIQCheckDrawings(c *gin.Context) {
	ids, err := parseIQCheckDrawingIDs(c.Query("ids"))
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	drawings, err := h.iqCheck.ListDrawings(c.Request.Context(), ids)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, drawings)
}

func parseIQCheckDrawingIDs(raw string) ([]int64, error) {
	ids := []int64{}
	seen := map[int64]bool{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("invalid account id: %q", part)
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if len(ids) > iqCheckDrawingsMaxIDs {
		return nil, fmt.Errorf("at most %d account ids per request", iqCheckDrawingsMaxIDs)
	}
	return ids, nil
}
