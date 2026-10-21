package apihttp_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hexagon-codes/hexclaw/scenarios/k12"
)

// 使用真实HTTP和持久化装配验证入口枚举；求解仍沿既有协议夹具，不证明学科答案质量。
func TestElementarySubjectsReachSolveAPI(t *testing.T) {
	server := httptest.NewServer(newServer(t))
	defer server.Close()
	for _, subject := range []string{"科学", "信息科技"} {
		t.Run(subject, func(t *testing.T) {
			body := `{"agent":"mingming","subject":"` + subject + `","problem":"依据题面条件解释结果"}`
			response, err := server.Client().Post(server.URL+"/solve", "application/json", strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			data, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("subject=%s HTTP=%d body=%s", subject, response.StatusCode, data)
			if response.StatusCode != http.StatusOK {
				t.Fatalf("available elementary subject was rejected: %s", data)
			}
			if k12.SubjectVerifierGatePassed(subject) {
				t.Fatal("route support unexpectedly promoted exercise verifier qualification")
			}
		})
	}
}
