// Swagger/OpenAPI general API information. Annotations on the handlers in
// this package describe every public endpoint. Generating the spec and
// serving Swagger UI (GET /swagger/*) requires the swaggo toolchain and is
// wired by the composition root in a later phase (dependency approval).

// @title           Task Management API
// @version         1.0
// @description     REST API for users, projects, memberships, and tasks.
// @description     Authorization is enforced in the service layer; access
// @description     tokens are short-lived HS256 JWTs (typ=access), refresh
// @description     tokens are opaque and rotate with reuse detection.
// @BasePath        /
// @accept          json
// @produce         json

// @securityDefinitions.apikey BearerAuth
// @in                         header
// @name                       Authorization
// @description                Enter: Bearer <access_token>
package handler
