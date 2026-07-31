package response

import "testing"

func TestErrorText(t *testing.T) {
	cases := []struct {
		name string
		res  *ApiResponse[any]
		want string
	}{
		{
			name: "message only — the half FirstError used to drop",
			res:  &ApiResponse[any]{Message: "card not found"},
			want: "card not found",
		},
		{
			name: "errors only",
			res:  &ApiResponse[any]{Errors: []string{"card not found"}},
			want: "card not found",
		},
		{
			name: "both, saying different things",
			res:  &ApiResponse[any]{Message: "could not recharge card", Errors: []string{"card 0503006304 is not assigned to a user"}},
			want: "could not recharge card; card 0503006304 is not assigned to a user",
		},
		{
			name: "both, saying the same thing — not repeated",
			res:  &ApiResponse[any]{Message: "card not found", Errors: []string{"card not found"}},
			want: "card not found",
		},
		{
			name: "every validation error, not just the first",
			res:  &ApiResponse[any]{Errors: []string{"amount is required", "serialNumber is required"}},
			want: "amount is required; serialNumber is required",
		},
		{
			name: "blank entries ignored",
			res:  &ApiResponse[any]{Message: "  ", Errors: []string{"", "real problem"}},
			want: "real problem",
		},
		{
			name: "says nothing at all — falls back",
			res:  &ApiResponse[any]{},
			want: "401 Unauthorized",
		},
		{
			name: "nil response — falls back",
			res:  nil,
			want: "401 Unauthorized",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ErrorText(c.res, "401 Unauthorized"); got != c.want {
				t.Errorf("ErrorText() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestFirstErrorStillOnlyReadsErrors(t *testing.T) {
	res := &ApiResponse[any]{Message: "card not found"}
	if got := FirstError(res, "fallback"); got != "fallback" {
		t.Errorf("FirstError() = %q, want %q — its documented behaviour must not change", got, "fallback")
	}
}
