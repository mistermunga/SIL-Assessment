# Appointment Management System - Savannah Informatics Limited

## Initial Considerations

I started by deriving functional and non-functional requirements from the problem statement. This let me state my assumptions explicitly and gave the design a solid foundation.

### Assumptions

- Each appointment occupies exactly one time slot.
- Time slots are always 30 minutes long.
- Doctors can schedule breaks as needed, capped at one hour per day.
- Schedules may differ between working days.
- All staff who operate the system are doctors.
- Leave is approved by an administrator.
- Payment integration is out of scope.

### Functional Requirements

1. Doctor management:
    - Doctors can set their own working hours for each day of the week.
    - Doctors can define their own break times.
    - Appointment slots must not overlap with each other or with break times.
1. Patient appointment booking:
    - Patients can view a doctor's availability.
    - Patients can book a specific slot with a doctor.
    - Patients can cancel their own future appointments at any time.
    - Two patients cannot book the same doctor for the same slot.
    - The system distinguishes missed appointments from completed ones.
1. Account management:
    - Administrators can create staff (doctor) accounts.
    - Administrators can delete any account.
    - Administrators can record leaves of absence for any doctor.
    - Patients can create their own accounts.

### Non-Functional Requirements

1. The application should have low latency.
1. Test coverage must be broad and cover all functional requirements.

## System Design

With the requirements settled, I moved on to design. Before choosing any technology, I defined the schema.

### Entity Relationships

The requirements imply two types of users: patients and doctors. The two overlap significantly, since both need biographical and identifying information for security and authentication. The schema therefore has a `users` table for the shared fields, and its primary key identifies the user in the `patients` and `doctors` tables.

![Planned Schema](planned-schema.png)

The `doctors` table has a flag marking administrators, who can perform user management. Its `doctor_id` field is a foreign key in two tables:

- `doctor_schedules`, which tracks each doctor's workdays and working hours, as well as their scheduled break time.
- `doctor_time_off`, which tracks when a doctor is unavailable.

The `appointments` table is the associative entity between doctors and patients.

At the database level, I planned constraints to prevent double bookings. Enforcing this in the database avoids race conditions when two patients book the same doctor at the same moment. A second constraint ensures each doctor has only one schedule per weekday.

### Application Design

With a clear picture of the database schema, I moved on to the macro structure of the application. Given the scale of the project, a monolith architecture suffices. Monolithic applications classically have three layers, arranged thus: (the Internet) -> Presentation Layer -> Business Logic -> Data Access -> (Database). Leaning on best practices, I had a good idea of what each layer should do:

| Application Layer | Purpose |
|:---:|:---:|
| Presentation Layer | Routing; this is where endpoints are defined. This layer is also the first line of defence: requests are authenticated here before reaching the Business Logic. |
| Business Logic | The core of the application, where most of the functional requirements are met. Usually built around "services", each with a specific scope. |
| Data Access | The interface between the Business Logic and the database. Connections and queries are created and managed here. |

### Security

Several user authentication technologies are in use in the industry, each with its own pros and cons. The right choice depends on the needs of the application. This is a healthcare application, so privacy is paramount.

**Authentication.** I chose stateless authentication with JSON Web Tokens (JWTs). The server holds no session state, which keeps overhead and latency low. I ruled out OAuth for three reasons:

1. **Privacy:** it shares user identity with a third-party provider, which is undesirable for health data.
1. **Access:** some users will log in from work machines and may not have third-party accounts.
1. **Dependency:** it adds an external dependency that the workplace has not agreed to.

**Token design.**

- Access tokens are short-lived (15 minutes) and carry the user ID and role (`patient`, `doctor`, `admin`).
- Tokens are signed (RS256) with a secret loaded from the environment, never committed.
- All traffic is served over HTTPS.

**Credentials.** Passwords are hashed with Bcrypt and never stored or logged in plaintext.

**Authorization.** Role-Based Access Control is enforced at the Presentation Layer using the role claim. Ownership checks need data and are enforced in the Business Logic.

**Trade-off.** Stateless tokens cannot be revoked before they expire, so a deleted account stays valid until its token lapses. The short expiry bounds this window. However, each request is authenticated, so the risk is minimized
