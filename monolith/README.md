[![License](https://img.shields.io/badge/License-MIT-green?style=for-the-badge)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.24.5-blue?style=for-the-badge&logo=go)](https://golang.org/)
[![PostgreSQL](https://img.shields.io/badge/PostgreSQL-15+-blue?style=for-the-badge&logo=postgresql)](https://postgresql.org/)

# 📋 Task Management API

> A production-ready task management REST API built as **Week 3** of my 8-week coding journey. Demonstrates advanced Go architecture, goroutines, real-time email notifications, and scalable backend design.

## ✨ **What This Project Demonstrates**

- **Advanced Go architecture** with clean separation of concerns and industry-standard project structure
- **Complete CRUD operations** for Projects and Tasks with sophisticated filtering and relationships
- **Async email notifications** using goroutines for non-blocking background processing
- **Production-grade security** with JWT authentication, input validation, and data integrity protection
- **Interactive API documentation** with Swagger/OpenAPI for seamless developer experience

## 🛠️ **Tech Stack**

| Category              | Technology                    | Purpose                               |
| --------------------- | ----------------------------- | ------------------------------------- |
| **Backend**           | Go 1.24.5 + Gin Framework    | High-performance HTTP server         |
| **Database**          | PostgreSQL + GORM            | Relational database with ORM         |
| **Authentication**    | JWT + bcrypt                  | Secure token-based authentication    |
| **Email**             | SMTP + Gmail                  | Real-time email notifications        |
| **Documentation**     | Swagger/OpenAPI               | Interactive API documentation        |
| **Deployment**        | Railway + Neon                | Cloud hosting with managed database   |

## 🏗️ **Architecture Highlights**

### **Project Structure**
```
cmd/server/           # Application entry point
internal/
├── handlers/         # HTTP request handlers
├── middleware/       # Authentication & validation middleware
├── models/          # Database models with GORM
├── services/        # Business logic & email service
├── database/        # Database connection & configuration
└── utils/           # Shared utilities
docs/                # Auto-generated Swagger documentation
```

### **Database Schema**
```sql
users: id, name, email, password_hash, role, timestamps
     
projects: id, name, description, owner_id, timestamps (soft delete)
     
tasks: id, title, description, project_id, assignee_id, creator_id, 
       status, priority, estimate, due_date, timestamps (soft delete)
```

### **Real-Time Email System**
- **Async processing** with goroutines for instant API responses
- **Beautiful HTML templates** with responsive design and priority color coding
- **Change tracking** showing detailed before/after values
- **SMTP integration** with Gmail for reliable delivery

## 🎯 **API Features**

### **Authentication System**
- **User registration** with password hashing and validation
- **JWT-based login** with secure token generation
- **Profile management** with duplicate email prevention
- **Protected routes** with middleware authentication

### **Projects Management**
- **Complete CRUD** operations with ownership validation
- **Duplicate prevention** ensuring unique project names per user
- **Cascading delete protection** preventing data loss
- **Owner relationship** with automatic assignment

### **Advanced Task System**
- **Rich task model** with status, priority, estimate, and due dates
- **Project relationships** with transfer capabilities between owned projects
- **Advanced filtering** by project, status, priority, estimate, and date ranges
- **Enum validation** with clear error messages for invalid values
- **Change tracking** for email notifications

## 📊 **API Endpoints**

### **Authentication**
```
POST   /auth/register     # User registration
POST   /auth/login        # User login
GET    /users/me          # Get current user profile
PUT    /users/me          # Update user profile
```

### **Projects**
```
POST   /projects          # Create new project
GET    /projects          # List user's projects
GET    /projects/:id      # Get project details with task count
PUT    /projects/:id      # Update project information
DELETE /projects/:id      # Delete project (only if no tasks exist)
```

### **Tasks**
```
POST   /tasks             # Create new task with email notification
GET    /tasks             # List tasks with advanced filtering
GET    /tasks/:id         # Get detailed task information
PUT    /tasks/:id         # Update task with change notifications
DELETE /tasks/:id         # Delete task
```

### **Advanced Task Filtering**
```
GET /tasks?project_id=1&status=In%20Progress&priority=High&estimate=L&due_date_from=2025-07-25&due_date_to=2025-07-31
```

## 🚀 **Getting Started**

### **Prerequisites**
- Go 1.24.5+
- PostgreSQL database (or Neon account)
- Gmail account with App Password for email notifications

### **Local Development**
```bash
# Clone the repository
git clone https://github.com/P4rz1val22/task-management-api.git
cd task-management-api

# Install dependencies
go mod download

# Set up environment variables
cp .env.example .env
# Edit .env with your credentials

# Run database migrations
go run cmd/server/main.go

# Generate Swagger documentation
go run github.com/swaggo/swag/cmd/swag@latest init -g cmd/server/main.go

# Start development server
go run cmd/server/main.go
```

### **Environment Variables**
```env
# Database Configuration
DATABASE_URL=your_postgresql_connection_string

# JWT Configuration
JWT_SECRET=your_secure_jwt_secret_key

# Email Configuration (Optional)
SMTP_USERNAME=your-gmail@gmail.com
SMTP_PASSWORD=your-gmail-app-password

# Server Configuration
GIN_MODE=debug
PORT=8080
```

### **Gmail Email Setup**
1. **Enable 2-Factor Authentication** on your Gmail account
2. **Generate App Password**:
    - Go to [myaccount.google.com/apppasswords](https://myaccount.google.com/apppasswords)
    - Select "Mail" → Generate → Copy 16-character password
3. **Set environment variables** with your Gmail and app password


## 📈 **Key Achievements**

- **Goroutines mastery**: Non-blocking email processing with async patterns
- **Production security**: JWT authentication, input validation, and SQL injection prevention
- **Data integrity**: Cascading delete protection and ownership validation
- **Developer experience**: Interactive Swagger documentation with authentication support
- **Email reliability**: SMTP integration with error handling and retry logic
- **Advanced filtering**: Complex query building with multiple filter combinations

## 🔒 **Security Features**

- **Password hashing** with bcrypt for secure storage
- **JWT tokens** with configurable expiration and secure signing
- **Input validation** with comprehensive error messages
- **SQL injection prevention** through GORM's parameterized queries
- **Ownership validation** ensuring users can only access their own data

## 📑 **API Documentation**
Live interactive documentation available at: `https://task-management-api-production-0512.up.railway.app/swagger/index.html`

## 📝 **License**

MIT License - see the [LICENSE](LICENSE) file for details.

---

**Built with ❤️ by Luis** | **Part of 8-Week Coding Journey** | **Week 3: Advanced Backend Development**