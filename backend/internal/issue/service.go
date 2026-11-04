package issue

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/kataras/iris/v12"
)

// Register 把 Issue HTTP API 挂到 Iris 应用：
//
//	POST   /api/issues                     创建
//	GET    /api/issues?q=&status=&limit=   检索（相似度降序）
//	GET    /api/issues/{id}                详情
//	POST   /api/issues/{id}/comments       追加评论 {author,text}
//	POST   /api/issues/{id}/status         状态流转 {status}
func Register(app iris.Party, st Store) {
	p := app.Party("/api/issues")

	p.Post("/", func(ctx iris.Context) {
		var in CreateInput
		if err := ctx.ReadJSON(&in); err != nil || strings.TrimSpace(in.Title) == "" {
			fail(ctx, http.StatusBadRequest, "bad request: title required")
			return
		}
		iss, err := st.Create(ctx, in)
		if err != nil {
			internal(ctx, err)
			return
		}
		ctx.StatusCode(http.StatusCreated)
		ctx.JSON(iss)
	})

	p.Get("/", func(ctx iris.Context) {
		q := ctx.URLParamDefault("q", "")
		limit, _ := strconv.Atoi(ctx.URLParamDefault("limit", "5"))
		list, err := st.Search(ctx, q, limit)
		if err != nil {
			internal(ctx, err)
			return
		}
		ctx.JSON(list)
	})

	p.Get("/{id:string}", func(ctx iris.Context) {
		iss, err := st.Get(ctx, ctx.Params().Get("id"))
		if err != nil {
			notFoundOrInternal(ctx, err)
			return
		}
		ctx.JSON(iss)
	})

	p.Post("/{id:string}/comments", func(ctx iris.Context) {
		var body struct {
			Author string `json:"author"`
			Text   string `json:"text"`
		}
		if err := ctx.ReadJSON(&body); err != nil || body.Text == "" {
			fail(ctx, http.StatusBadRequest, "bad request: text required")
			return
		}
		iss, err := st.AddComment(ctx, ctx.Params().Get("id"), body.Author, body.Text)
		if err != nil {
			notFoundOrInternal(ctx, err)
			return
		}
		ctx.JSON(iss)
	})

	p.Post("/{id:string}/status", func(ctx iris.Context) {
		var body struct {
			Status Status `json:"status"`
		}
		if err := ctx.ReadJSON(&body); err != nil || body.Status == "" {
			fail(ctx, http.StatusBadRequest, "bad request: status required")
			return
		}
		iss, err := st.UpdateStatus(ctx, ctx.Params().Get("id"), body.Status)
		if err != nil {
			notFoundOrInternal(ctx, err)
			return
		}
		ctx.JSON(iss)
	})
}

func notFoundOrInternal(ctx iris.Context, err error) {
	if errors.Is(err, ErrNotFound) {
		fail(ctx, http.StatusNotFound, "issue not found")
		return
	}
	internal(ctx, err)
}

func fail(ctx iris.Context, code int, msg string) {
	ctx.StatusCode(code)
	_, _ = ctx.WriteString(msg)
}

func internal(ctx iris.Context, err error) {
	ctx.StatusCode(http.StatusInternalServerError)
	ctx.JSON(map[string]string{"error": err.Error()})
}
