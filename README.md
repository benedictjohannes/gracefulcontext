# GracefulContext

Package `gracefulcontext` provides an implementation of go's context.Context with cleanup and cleanup timeout functionalities.

## Rationale

The standard library `context` is an excellent system to provide lifetime management for goroutines and services, where cancellation of a context means the associated goroutines should stop their works.

However, in some places, the standard `context`:

-   requires a specifying a listener to the `Done()` channel in a goroutine to cancel a resource associated with the `Context`.
-   lacks the ability to monitor the cancellation (cancellations are immediate)
-   lacks the ability to subscribe to another context's cancellation by itself

This packages aims to augment the standard `Context` by adding the above features.

## Usage

The package adds the ability to perform cleanup orchestration. Below is its simple usage in a `main()` function:

```go
package main

import (
    "context"
    "gracefulcontext"
    "time"
    "os"
    "os/signal"
    "syscall"
    // arbitrary libraries
    "yourpackage/http"
    "yourpackage/database"
    "yourpackage/logs"
)
func main() {
    bgCtx := context.Background()
    // want logs to be last exitter
    logCtx := gracefulcontext.NewGracefulContext(bgCtx).WithCleanupFunc(logs.Close()).ImmediatelyPropagateCancel(true).Make()
    // max 5s cleanup timeout for database termination
    dbCtx := gracefulcontext.NewGracefulContext(logCtx).WithCleanupFunc(db.Connections.Close()).WithCleanupTimeout(time.Duration(5) * time.Second).Make()
    // max 2s for HTTP termination
    httpGracefulCtx := gracefulcontext.NewGracefulContext(logCtx).WithCleanupFunc(http.Server.Stop()).WithCleanupTimeout(time.Duration(2) * time.Second)
    // as context.Context() interface with context.WithCancel() cancel function
    httpCtx, cancelHttp := httpCtx.Context()
    // SUBSCRIPTIONS
    // start database closure after the HTTP server is terminated
    dbCtx.SubscribeCancellation(httpCtx)
    // start log closure after the database server is terminated
    logCtx.SubscribeCancellation(dbCtx)
    dbConnection := connectDatabase(dbCtx)
    http.Init(httpCtx, dbConnection)
    // start closing resources on SIGINT, starting from HTTP
    sigIntChan := make(chan os.Signal, 1)
	signal.Notify(sigIntChan, syscall.SIGINT)
    go func () {
        <-sigIntChan
        // same as httpCtx.Cancel()
        cancelHttp
        // when the http.Server.Stop() or its cleanup completes, it will initiate database context cancellation
        // after database cleanup/timer completes, will cancel the logCtx
        // all resources are cleaned up gracefully
    }()
    // start closing resources on SIGTERM, ASAP
    sigTermChan := make(chan os.Signal, 1)
	signal.Notify(sigTermChan, syscall.SIGTERM)
    go func () {
        <-sigIntChan
        // propagates cancellation immediately to dbCtx and httpCtx
        // as ImmediatelyPropagateCancel is true
        logCancel() // same as calling logGracefulCtx.Cancel()
    }()
    go func () {
        <-logCtx.CleanupDone()
        // only exit the process after logs are closed
        os.Exit(0)
    }()
}
```

Note that due to the flexibility offered by this package, it will consume more resources like starting more goroutines and creating more channels than the standard library's `context`, as it has to listen for parent context cancellation. To minimize tradeoffs, creating another `context.WithCancel` that is assigned as parent of contexts that will be created and teardown quickly is likely to provide performance benefits.

## Contributing

Pull requests are truly welcomed! I will be monitoring this repository as I will be using it for production servers. This is still a plain repository without CI/CD steps, so, if you would like to contribute a PR, making sure that it has passed the tests will be very helpful. 

## Versioning

This package is still not released as a stable version, as I would like to see more usage in production scenarios.

Nonetheless, the existing API will most likely never change, while addition of features are still likely.
