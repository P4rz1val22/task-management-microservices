# Task Management Microservices

> **Week 4 Project**: Migration from monolithic architecture to microservices using the Strangler Fig Pattern

## 🏗️ Architecture Overview

This project demonstrates the evolution from a monolithic API to a microservices architecture, showcasing industry-standard patterns for service decomposition and API gateway routing.

```
┌─────────────────────────────────────────────────────────────┐
│                     Client Applications                     │
└─────────────────────┬───────────────────────────────────────┘
                      │
                      ▼
┌─────────────────────────────────────────────────────────────┐
│                  API Gateway                                │
│                (Port 8081)                                 │
│                                                             │
│  Smart Routing:                                             │
│  • /auth/*     → Auth Service (8082)                      │
│  • /projects/* → Project Service (8083)                   │
│  • /tasks/*    → Task Service (8084)                      │
│  • /users/*    → Monolith (8080)                         │
└─────────────┬─────────────┬─────────────┬─────────────────┘
              │             │             │
              ▼             ▼             ▼
┌─────────────────┐ ┌─────────────────┐ ┌─────────────────┐
│  Auth Service   │ │ Project Service │ │   Task Service  │
│   (Port 8082)   │ │   (Port 8083)   │ │   (Port 8084)   │
└─────────────────┘ └─────────────────┘ └─────────────────┘
              │             │             │
              └─────────────┼─────────────┘
                            ▼
                  ┌─────────────────┐
                  │   Monolith      │
                  │  (Port 8080)    │
                  │                 │
                  │ • User Profile  │
                  │ • Legacy APIs   │
                  └─────────────────┘
                            │
                            ▼
                  ┌─────────────────┐
                  │   PostgreSQL    │
                  │  (Shared DB)    │
                  └─────────────────┘
```

## 🚀 Quick Start

### Prerequisites
- Go 1.24.5+
- PostgreSQL (Neon/local)
- Environment variables configured

### Run All Services
```bash
# Start all services with Docker Compose
docker-compose up -d

# OR manually start each service:
# Terminal 1: Gateway
cd gateway && go run main.go

# Terminal 2: Auth Service  
cd auth-service && go run main.go

# Terminal 3: Project Service
cd project-service && go run main.go

# Terminal 4: Task Service
cd task-service && go run main.go

# Terminal 5: Monolith
cd monolith && go run cmd/server/main.go
```

### Verify Everything Works
```bash
# Check all services are healthy
curl http://localhost:8081/gateway/health

# Expected response shows all services as "healthy"
```

## 📁 Repository Structure

```
task-management-microservices/
├── README.md                    # This file
├── docker-compose.yml           # Multi-service orchestration
├── docs/                       # Architecture documentation
│   ├── api-documentation.md    # Complete API reference
│   └── deployment-guide.md     # Production deployment guide
├── gateway/                    # API Gateway (Port 8081)
│   ├── internal/proxy/         # Request routing logic
│   └── main.go                 # Gateway server
├── auth-service/               # Authentication Service (Port 8082)
│   ├── internal/handlers/      # Auth endpoints
│   ├── pkg/utils/             # JWT utilities
│   └── main.go                # Auth server
├── project-service/            # Project Management (Port 8083)
│   ├── internal/handlers/      # Project CRUD operations
│   ├── internal/models/        # Project data models
│   └── main.go                # Project server
├── task-service/              # Task Management (Port 8084)
│   ├── internal/handlers/      # Task CRUD + filtering
│   ├── internal/models/        # Task data models
│   └── main.go                # Task server
└── monolith/                  # Original Monolithic API (Port 8080)
    ├── cmd/server/            # Monolith entry point
    ├── internal/              # Monolith business logic
    └── docs/                  # Swagger documentation
```

## 🔧 Services Overview

### 1. API Gateway (Port 8081)
**Responsibility**: Intelligent request routing and service orchestration

**Key Features**:
- URL-based routing (`/auth/*`, `/projects/*`, `/tasks/*`)
- Service health monitoring and aggregation
- Request/response logging
- Error handling and fallback strategies

**Technology**: Go + Gin + Reverse Proxy

### 2. Auth Service (Port 8082)
**Responsibility**: User authentication and JWT token management

**Endpoints**:
- `POST /auth/register` - User registration
- `POST /auth/login` - User authentication
- `GET /health` - Service health check

**Key Features**:
- JWT token generation (24-hour expiration)
- Password hashing with bcrypt
- User registration with duplicate checking

### 3. Project Service (Port 8083)
**Responsibility**: Project management and ownership

**Endpoints**:
- `GET /projects` - List user's projects
- `POST /projects` - Create new project
- `GET /projects/:id` - Get project details with task count
- `PUT /projects/:id` - Update project
- `DELETE /projects/:id` - Delete project (if no tasks exist)

**Key Features**:
- Project ownership validation
- Cross-service data enrichment (user names, task counts)
- JWT-based authorization

### 4. Task Service (Port 8084)
**Responsibility**: Task management with advanced filtering

**Endpoints**:
- `GET /tasks` - List and filter tasks
- `POST /tasks` - Create new task
- `GET /tasks/:id` - Get task details
- `PUT /tasks/:id` - Update task
- `DELETE /tasks/:id` - Delete task

**Key Features**:
- Complex filtering (project, status, priority, due dates)
- Cross-service data enrichment (project names, user names)
- Advanced validation (status, priority, estimate)
- Authorization checks (project ownership)

**Filter Parameters**:
```bash
GET /tasks?project_id=1&status=In Progress&priority=High&due_date_from=2025-01-01
```

### 5. Monolith (Port 8080)
**Responsibility**: Legacy functionality not yet extracted

**Current Endpoints**:
- `GET /users/me` - User profile management
- `PUT /users/me` - Update user profile
- All other non-auth, non-project, non-task endpoints

## 🔒 Authentication Flow

### JWT Token Lifecycle
```
1. Client → Gateway → Auth Service: POST /auth/login
2. Auth Service: Validates credentials, creates JWT
3. Auth Service → Gateway → Client: Returns JWT token
4. Client → Gateway: Subsequent requests with Authorization header
5. Gateway → Target Service: Forwards request with JWT
6. Target Service: Validates JWT independently
7. Target Service → Gateway → Client: Returns authorized response
```

**Security Features**:
- Shared JWT secret across all services
- 24-hour token expiration
- User ID and email in token claims
- Bearer token validation middleware

## 📊 Data Strategy

### Shared Database Approach
- **Single PostgreSQL instance** serves all services
- **Gradual migration** without data duplication
- **Consistent relationships** across service boundaries

**Database Tables**:
- `users` - User accounts and authentication
- `projects` - Project information and ownership
- `tasks` - Task details with project/user relationships

## 🧪 Testing

### Automated Testing (Postman)
Complete test suite available in `/docs/postman-collection.json`:

- ✅ Service health checks
- ✅ Authentication flow (register/login)
- ✅ Project CRUD operations
- ✅ Task CRUD with complex filtering
- ✅ Cross-service data validation
- ✅ JWT token lifecycle
- ✅ End-to-end workflow validation

### Manual Testing
```bash
# 1. Login and get JWT token
curl -X POST http://localhost:8081/auth/login \
  -H "Content-Type: application/json" \
  -d '{"email": "user@example.com", "password": "password123"}'

# 2. Create project (routed to Project Service)
curl -X POST http://localhost:8081/projects \
  -H "Authorization: Bearer <JWT_TOKEN>" \
  -H "Content-Type: application/json" \
  -d '{"name": "Test Project", "description": "Testing microservices"}'

# 3. Create task (routed to Task Service)
curl -X POST http://localhost:8081/tasks \
  -H "Authorization: Bearer <JWT_TOKEN>" \
  -H "Content-Type: application/json" \
  -d '{"title": "Test Task", "project_id": 1, "status": "In Progress"}'

# 4. Filter tasks (advanced filtering)
curl -H "Authorization: Bearer <JWT_TOKEN>" \
  "http://localhost:8081/tasks?status=In Progress&priority=High"
```

## 🚀 Deployment

### Local Development
```bash
# Start all services
docker-compose up -d

# View logs
docker-compose logs -f

# Stop all services
docker-compose down
```

### Production Deployment
See `/docs/deployment-guide.md` for:
- Containerization with Docker
- Kubernetes deployment manifests
- CI/CD pipeline configuration
- Monitoring and observability setup

## 📈 Performance Considerations

### Latency Impact
- **Additional hop**: Client → Gateway → Service adds ~1-2ms
- **Network overhead**: HTTP proxying instead of direct calls
- **Service startup**: Multiple processes vs single monolith

### Scalability Benefits
- **Independent scaling**: Scale services based on demand
- **Resource isolation**: Memory/CPU per service
- **Deployment isolation**: Update services independently

## 🔄 Migration Strategy (Strangler Fig Pattern)

### Phase 1: Foundation ✅ COMPLETE
- [x] API Gateway with intelligent routing
- [x] Auth Service extraction
- [x] Project Service extraction
- [x] Task Service extraction
- [x] Shared database strategy

### Phase 2: Advanced Services (Future)
- [ ] Notification Service for emails
- [ ] User Profile Service

## 🛠️ Technology Stack

**Languages & Frameworks**:
- Go 1.24.5
- Gin HTTP framework
- GORM ORM

**Infrastructure**:
- PostgreSQL database
- JWT authentication
- Docker containerization
- Reverse proxy routing

**Testing & Documentation**:
- Postman automated tests
- Swagger API documentation
- Comprehensive logging

## 📚 Learning Outcomes

### Microservices Patterns Demonstrated
1. **API Gateway Pattern** - Single entry point with intelligent routing
2. **Strangler Fig Pattern** - Gradual migration from monolith
3. **Shared Database** - Pragmatic approach to service extraction
4. **Service Authentication** - JWT token validation across services
5. **Health Check Aggregation** - Centralized service monitoring

### Industry Best Practices Applied
- **Zero-downtime migration** approach
- **API compatibility** preservation
- **Independent deployability** of services
- **Separation of concerns** by business domain
- **Comprehensive testing** strategy

## 🤝 Contributing

### Development Workflow
1. Start all services locally
2. Make changes to individual services
3. Test with Postman collection
4. Ensure all health checks pass
5. Update documentation as needed

### Adding New Services
1. Create new service directory
2. Follow existing patterns for structure
3. Update API Gateway routing
4. Add health check endpoint
5. Update docker-compose.yml
6. Add tests to Postman collection

## 📖 Documentation

- **API Reference**: `/docs/api-documentation.md`
- **Deployment Guide**: `/docs/deployment-guide.md`
- **Architecture Decisions**: `/docs/architecture-decisions.md`
- **Postman Collection**: `/docs/postman-collection.json`

## 🏆 Project Achievements

This project successfully demonstrates:
- ✅ **Service isolation** by business domain
- ✅ **Independent deployability** of components
- ✅ **Maintained API compatibility** for existing clients
- ✅ **Scalable architecture** foundation for future growth
- ✅ **Industry-standard patterns** and best practices
- ✅ **Comprehensive testing** and monitoring capabilities

---

**Built as part of an 8-week intensive coding journey - Week 4: Microservices Architecture**

*Showcasing the evolution from monolithic systems to distributed microservices using production-ready patterns and industry best practices.*