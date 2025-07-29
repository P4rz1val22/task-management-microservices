package proxy

import (
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
)

const (
	MONOLITH_BASE_URL   = "http://localhost:8080"
	AUTH_SERVICE_URL    = "http://localhost:8082"
	PROJECT_SERVICE_URL = "http://localhost:8083"
	TASK_SERVICE_URL    = "http://localhost:8084"
)

// SmartProxy routes requests to appropriate services
func SmartProxy() gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Request.URL.Path

		// Route /auth/* to Auth Service
		if strings.HasPrefix(path, "/auth/") {
			ForwardToAuthService(c)
			return
		}

		// Route /projects/* to Project Service
		if strings.HasPrefix(path, "/projects") {
			ForwardToProjectService(c)
			return
		}

		// Route /tasks/* to Task Service
		if strings.HasPrefix(path, "/tasks") {
			ForwardToTaskService(c)
			return
		}

		// Route everything else to Monolith
		ForwardToMonolith(c)
	}
}

// ForwardToAuthService forwards auth requests to the auth service
func ForwardToAuthService(c *gin.Context) {
	target, err := url.Parse(AUTH_SERVICE_URL)
	if err != nil {
		log.Printf("Failed to parse auth service URL: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Gateway configuration error"})
		return
	}

	proxy := httputil.NewSingleHostReverseProxy(target)

	proxy.Director = func(req *http.Request) {
		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
		req.Host = target.Host

		log.Printf("Gateway → Auth Service: %s %s", req.Method, req.URL.Path)
	}

	proxy.ErrorHandler = func(rw http.ResponseWriter, req *http.Request, err error) {
		log.Printf("Auth Service proxy error: %v", err)
		rw.WriteHeader(http.StatusBadGateway)
		rw.Write([]byte(`{"error": "Gateway: Auth service unavailable"}`))
	}

	proxy.ServeHTTP(c.Writer, c.Request)
}

// ForwardToTaskService forwards task requests to the task service
func ForwardToTaskService(c *gin.Context) {
	target, err := url.Parse(TASK_SERVICE_URL)
	if err != nil {
		log.Printf("Failed to parse task service URL: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Gateway configuration error"})
		return
	}

	proxy := httputil.NewSingleHostReverseProxy(target)

	proxy.Director = func(req *http.Request) {
		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
		req.Host = target.Host

		log.Printf("Gateway → Task Service: %s %s", req.Method, req.URL.Path)
	}

	proxy.ErrorHandler = func(rw http.ResponseWriter, req *http.Request, err error) {
		log.Printf("Task Service proxy error: %v", err)
		rw.WriteHeader(http.StatusBadGateway)
		rw.Write([]byte(`{"error": "Gateway: Task service unavailable"}`))
	}

	proxy.ServeHTTP(c.Writer, c.Request)
}

// ForwardToProjectService forwards project requests to the project service
func ForwardToProjectService(c *gin.Context) {
	target, err := url.Parse(PROJECT_SERVICE_URL)
	if err != nil {
		log.Printf("Failed to parse project service URL: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Gateway configuration error"})
		return
	}

	proxy := httputil.NewSingleHostReverseProxy(target)

	proxy.Director = func(req *http.Request) {
		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
		req.Host = target.Host

		log.Printf("Gateway → Project Service: %s %s", req.Method, req.URL.Path)
	}

	proxy.ErrorHandler = func(rw http.ResponseWriter, req *http.Request, err error) {
		log.Printf("Project Service proxy error: %v", err)
		rw.WriteHeader(http.StatusBadGateway)
		rw.Write([]byte(`{"error": "Gateway: Project service unavailable"}`))
	}

	proxy.ServeHTTP(c.Writer, c.Request)
}

// ForwardToMonolith forwards non-auth requests to the monolith
func ForwardToMonolith(c *gin.Context) {
	target, err := url.Parse(MONOLITH_BASE_URL)
	if err != nil {
		log.Printf("Failed to parse monolith URL: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Gateway configuration error"})
		return
	}

	proxy := httputil.NewSingleHostReverseProxy(target)

	proxy.Director = func(req *http.Request) {
		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
		req.Host = target.Host

		log.Printf("Gateway → Monolith: %s %s", req.Method, req.URL.Path)
	}

	proxy.ErrorHandler = func(rw http.ResponseWriter, req *http.Request, err error) {
		log.Printf("Monolith proxy error: %v", err)
		rw.WriteHeader(http.StatusBadGateway)
		rw.Write([]byte(`{"error": "Gateway: Monolith service unavailable"}`))
	}

	proxy.ServeHTTP(c.Writer, c.Request)
}

// HealthCheck provides gateway health status
func HealthCheck(c *gin.Context) {
	// Test connection to all services
	monolithResp, monolithErr := http.Get(MONOLITH_BASE_URL + "/health")
	authResp, authErr := http.Get(AUTH_SERVICE_URL + "/health")
	projectResp, projectErr := http.Get(PROJECT_SERVICE_URL + "/health")
	taskResp, taskErr := http.Get(TASK_SERVICE_URL + "/health")

	var monolithStatus, authStatus, projectStatus, taskStatus string

	// Check monolith status
	if monolithErr != nil {
		monolithStatus = "unreachable"
	} else {
		monolithResp.Body.Close()
		if monolithResp.StatusCode == 200 {
			monolithStatus = "healthy"
		} else {
			monolithStatus = "unhealthy"
		}
	}

	// Check auth service status
	if authErr != nil {
		authStatus = "unreachable"
	} else {
		authResp.Body.Close()
		if authResp.StatusCode == 200 {
			authStatus = "healthy"
		} else {
			authStatus = "unhealthy"
		}
	}

	// Check project service status
	if projectErr != nil {
		projectStatus = "unreachable"
	} else {
		projectResp.Body.Close()
		if projectResp.StatusCode == 200 {
			projectStatus = "healthy"
		} else {
			projectStatus = "unhealthy"
		}
	}

	// Check task service status
	if taskErr != nil {
		taskStatus = "unreachable"
	} else {
		taskResp.Body.Close()
		if taskResp.StatusCode == 200 {
			taskStatus = "healthy"
		} else {
			taskStatus = "unhealthy"
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"gateway_status":         "healthy",
		"monolith_status":        monolithStatus,
		"auth_service_status":    authStatus,
		"project_service_status": projectStatus,
		"task_service_status":    taskStatus,
		"gateway_port":           "8081",
		"services": gin.H{
			"monolith":        MONOLITH_BASE_URL,
			"auth_service":    AUTH_SERVICE_URL,
			"project_service": PROJECT_SERVICE_URL,
			"task_service":    TASK_SERVICE_URL,
		},
		"message": "API Gateway with full microservices routing",
		"routing": gin.H{
			"/auth/*":         "auth-service (port 8082)",
			"/projects/*":     "project-service (port 8083)",
			"/tasks/*":        "task-service (port 8084)",
			"everything_else": "monolith (port 8080)",
		},
	})
}
