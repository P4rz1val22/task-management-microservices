package proxy

import (
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
)

var (
	MONOLITH_BASE_URL   = getEnv("MONOLITH_URL", "http://localhost:8080")
	AUTH_SERVICE_URL    = getEnv("AUTH_SERVICE_URL", "http://localhost:8082")
	PROJECT_SERVICE_URL = getEnv("PROJECT_SERVICE_URL", "http://localhost:8083")
	TASK_SERVICE_URL    = getEnv("TASK_SERVICE_URL", "http://localhost:8084")
)

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

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

		c.JSON(http.StatusNotFound, gin.H{
			"error":            "Endpoint not found in microservices architecture",
			"available_routes": []string{"/auth/*", "/projects/*", "/tasks/*", "/gateway/health"},
		})
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

func HealthCheck(c *gin.Context) {
	authResp, authErr := http.Get(AUTH_SERVICE_URL + "/health")
	projectResp, projectErr := http.Get(PROJECT_SERVICE_URL + "/health")
	taskResp, taskErr := http.Get(TASK_SERVICE_URL + "/health")

	var authStatus, projectStatus, taskStatus string

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
		"auth_service_status":    authStatus,
		"project_service_status": projectStatus,
		"task_service_status":    taskStatus,
		"gateway_port":           "8081",
		"services": gin.H{
			"auth_service":    AUTH_SERVICE_URL,
			"project_service": PROJECT_SERVICE_URL,
			"task_service":    TASK_SERVICE_URL,
		},
		"message": "Pure microservices API Gateway",
		"routing": gin.H{
			"/auth/*":     "auth-service",
			"/projects/*": "project-service",
			"/tasks/*":    "task-service",
		},
	})
}
