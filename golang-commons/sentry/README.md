# Platform Mesh Sentry

The `sentry` package implements some helper functions to use in applications that want to send error captures to Sentry.

### Initialization

Init the Sentry connection at the start of our application e.g. in your main function. You need to provide a valid context
that is canceled when your application stops.

```go
ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGKILL, syscall.SIGINT)
defer cancel()

err := sentry.Start(ctx, "sentryDSN", "env", "region", "image", "image tag")
if err != nil {
    // handle error
}

```

The underlying Sentry SDK then runs in the background and flushes error capturings to Sentry.

### Capture Errors

Use the `CaptureError` function to send errors to Sentry. You have to provide the error and you can add tags and extra information.
Tags are string/string value pairs that help filter errors in Sentry. Extras can be any data types with a string as key that
are then displayed inside Sentry's error details.

```go
sentry.CaptureError(err, tags, extras)

```

`Tags` and `Extras` are helper data types defined in the Sentry package. Both have an `Add` method that can be used to easily
add new values like so:

```go
extras := sentry.Extras{}
tags := sentry.Tags{}

extras.Add("query", oc.RawQuery)
tags.Add("path", path.String())
```

### Sentry Error

The Sentry package contains an Error type that wraps the original Go error and can be used to distinguish between
errors that should be sent to Sentry and those that should not be sent.

```go
err := errors.New("test error")

// wrap any error to mark it an Sentry worthy error
sentryError := SentryError(err)

if IsSentryError(err) {
	// send it to Sentry
}
```

The idea behind this is, that if you collect errors in a central place in an application you can wrap errors
as `sentry.Error` to check if it should be sent to Sentry later. Wrapped errors using `fmt.Errorf` are also supported, so you can
wrap `sentry.Error` errors like so:

```go
err := errors.New("test error")

sentryError := SentryError(err)

// create a wrapped error
newErr := fmt.Errorf("added a new error: %w", sentryError)

sentryErr, ok := AsSentryError(newErr)
if ok {
	// sentryErr is loaded from the stack of all errors in the chain
}
```

*Important:*

`sentry.CaptureError` captures errors regardless if it is a `sentry.Error` or not, mainly for compatibility reasons.
But it uses provided tags and extras if the error is of `sentry.Error` type.

### Better Stack Traces

If you create a SentryError from an existing error the current stack trace is added. This is done by wrapping it as an
`ErrorWithStack` from `go.platform-mesh.io/golang-commons/errors`

You can use the `go.platform-mesh.io/golang-commons/errors` package as a drop-in replacement for the stdlib `errors` package
everywhere in your application. This way you get additional stack traces for every wrapped error in the Sentry UI.

### GraphQL Error Presenter

The package contains a function that returns an error presenter that can be used with the `github.com/99designs/gqlgen/graphql` stack.
It can be used in a GraphQL service like so:

```go
import (
    "go.platform-mesh.io/golang-commons/sentry"
)

gqHandler.SetErrorPresenter(sentry.GraphQLErrorPresenter())
```

The error presenter enriches the error sent to Sentry with all available information from the GraphQL query.
In addition, it only sends error that are wrapped as `sentry.Error`. If needed, one or more tenant IDs that should
be skipped when sending errors (e.g. E2E tenant) can be provided as arguments.

### Recover panics

There are rare circumstances where a Go program can crash with a panic. This happens if a `nil` pointer is de-referenced
or a not existent map index is accessed. In order to send these errors to Sentry and log them there is a `Recover` func in the Sentry package.
This function can be used in `main()` to record all panics (but not recover from them). It is also possible to use it in
functions that are likely to panic (and then recover without crashing). However, if the `Recover()` function is used in `main()` only,
it prevents not from crashing, it just logs the panic before crashing.

Please note that the `Recover` function has to be called with the `defer` keyword like so:
```go
package main
import (
	"context"
	"os/signal"
	"syscall"

    "go.platform-mesh.io/golang-commons/sentry"
    "go.platform-mesh.io/golang-commons/logger"
)

func main() {
    ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGKILL, syscall.SIGINT)
    defer cancel()

    defer sentry.Recover(logger.StdLogger)

    err := sentry.Start(ctx, "sentryDSN", "env", "region", "image", "image tag")
    if err != nil {
        // ...
    }

	// ...
}
```

It is important to notice that recovering in `main()` does *not* work for subsequent Go routines. This means you must use a dedicated
defer `Recover` function call for *every* Go routine. The same holds true for usage of the `http` server package as it spawns Go routines and comes with its
own recover handling. To circumvent this there is a HTTP middleware `Recoverer()` in this `Sentry` package.

```go
	router := chi.NewRouter()
	router.Use(logger.StoreLoggerMiddleware(log))
	router.Use(sentry.Recoverer)
```

For operators, a good place to use the `Recover()` would be the reconciler function. In this case, if there is any panic in the reconile process it
it is logged and the application can recover form it and does not crash.

There is an additional function that can be used as a GraphQL middleware to catch panics and handle them. In your service use it like this:

```go
    gqHandler := handler.NewDefaultServer(graphql.NewExecutableSchema(gql))
    gqHandler.SetRecoverFunc(sentry.GraphQLRecover(log))
```
