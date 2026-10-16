# Appointment Management System - Savannah Informatics Limited

## Initial Considerations

I started by deriving functional and non-functional requirements from the problem statement. This let me state my assumptions explicitly and gave the design a solid foundation.

### Assumptions

- The clinic operates Monday to Friday, in a single time zone.
- Each appointment occupies exactly one time slot.
- Time slots are always 30 minutes long and start on the hour or half hour.
- A doctor has at most one working shift per weekday, with at most one break inside it, capped at one hour. Break times fall on 30-minute boundaries.
- Schedules may differ between working days.
- All staff who operate the system are doctors. An administrator is a doctor with an admin flag.
- Each user holds exactly one role: patient or doctor.
- Leave is recorded by an administrator. Leave cannot be recorded over existing booked appointments; those must be cancelled first.
- Accounts are soft-deleted, so appointment history is preserved.
- Payment integration is out of scope.

### Functional Requirements

1. Doctor management:
    - Doctors can set their own working hours for each day of the week.
    - Doctors can define their own break time for each day.
    - Appointment slots must not overlap with each other, with break times, or with leave.
    - Doctors can mark a past appointment as a no-show.
1. Patient appointment booking:
    - Patients can view a doctor's free slots for a given day.
    - Patients can book a specific free slot with a doctor.
    - Patients can cancel their own future appointments at any time.
    - Two patients cannot book the same doctor for the same slot. A cancelled slot becomes bookable again.
    - The system distinguishes missed appointments from completed ones. A booked appointment in the past is completed unless marked as a no-show.
1. Account management:
    - Patients can create their own accounts.
    - Administrators can create staff (doctor) accounts.
    - Administrators can delete any account.
    - Administrators can record leaves of absence for any doctor.

### Non-Functional Requirements

1. Low latency: routine requests must not require more than the queries their function needs (no per-request role lookup on the hot path).
1. Correctness under concurrency: double bookings are prevented by the database, not by application checks.
1. Privacy: this is a healthcare application. Passwords are hashed, traffic is served over HTTPS, and secrets are never committed.
1. Test coverage must be broad and cover all functional requirements, including a concurrent-booking test: many simultaneous requests for the same slot must produce exactly one booking.

## System Design

With the requirements settled, I moved on to design. Before choosing any technology, I defined the schema.

### Entity Relationships

The requirements imply two types of users: patients and doctors. The two overlap significantly, since both need biographical and identifying information for security and authentication. The schema therefore has a `users` table for the shared fields, and its primary key identifies the user in the `patients` and `doctors` tables. These are one-to-one extension tables. A user's role is the extension table in which their row exists; there is no role column.

![Planned Schema](planned-schema.png)

The `doctors` table has a flag marking administrators, who can perform user management. Its `doctor_id` field is a foreign key in three tables:

- `doctor_schedules`, which tracks each doctor's working hours per weekday, as well as their scheduled break.
- `doctor_time_off`, which tracks date ranges when a doctor is unavailable.
- `appointments`.

The `appointments` table is the associative entity between doctors and patients. Its `status` is one of `booked`, `cancelled` or `no_show`. "Completed" is not stored; it is derived from a `booked` appointment whose start time has passed.

`users` carries a `deleted_at` timestamp for soft deletion.

Free slots are computed: 30-minute steps across the day's working hours, minus the break, minus leave, minus booked appointments.

### Database Constraints

Rules the database enforces, so they hold regardless of application bugs or race conditions:

| Constraint | Purpose |
|:---|:---|
| Unique `(doctor_id, appointment_start)` where `status = 'booked'` | Prevents double bookings. The second concurrent insert fails and is reported as "slot taken". Cancelled rows do not block rebooking. |
| Unique `(doctor_id, weekday)` on `doctor_schedules` | One schedule per doctor per weekday. |
| `weekday` between 1 and 5; `start_time < end_time` | Valid working days and hours. |
| Break start and end both set or both null; break inside working hours and at most one hour | Valid breaks. |
| `appointment_start` on a 30-minute boundary | Slots align to the grid. |
| `start_date <= end_date` on `doctor_time_off` | Valid leave ranges. |

Two rules span tables and cannot be expressed as a single-row constraint, so they are enforced in the booking transaction: a slot must fall inside the doctor's working hours and outside the break and any leave.

### Application Design

With a clear picture of the database schema, I moved on to the macro structure of the application. Given the scale of the project, a monolith architecture suffices. Monolithic applications classically have three layers, arranged thus: (the Internet) -> Presentation Layer -> Business Logic -> Data Access -> (Database). Leaning on best practices, I had a good idea of what each layer should do:

| Application Layer | Purpose |
|:---:|:---:|
| Presentation Layer | Routing; this is where endpoints are defined. This layer is also the first line of defence: requests are authenticated and role-checked here before reaching the Business Logic. |
| Business Logic | The core of the application, where most of the functional requirements are met. Usually built around "services", each with a specific scope. Ownership checks and cross-table rules live here. |
| Data Access | The interface between the Business Logic and the database. Connections and queries are created and managed here. All queries are parameterised; user input is never concatenated into SQL. |

### Security

Several user authentication technologies are in use in the industry, each with its own pros and cons. The right choice depends on the needs of the application. This is a healthcare application, so privacy is paramount.

**Authentication.** I chose stateless authentication with JSON Web Tokens (JWTs). The server holds no session state, which keeps overhead and latency low. I ruled out OAuth for three reasons:

1. **Privacy:** it shares user identity with a third-party provider, which is undesirable for health data.
1. **Access:** some users will log in from work machines and may not have third-party accounts.
1. **Dependency:** it adds an external dependency that the workplace has not agreed to.

**Token design.**

- Access tokens are short-lived (15 minutes) and carry the user ID and role (`patient`, `doctor`, `admin`).
- Tokens are signed with HS256 using a random secret of at least 32 bytes, loaded from the environment and never committed.
- All traffic is served over HTTPS.

**Role derivation.** Role is derived from which extension table holds the user's row (`doctors`, with `is_admin` distinguishing `admin`, or `patients`). The login query fetches the user and joins both tables in one round trip, so the role is known at login and placed in the token. Registration inserts the `users` row and its extension row in one transaction, so a user never exists without a role.

**Credentials.** Passwords are hashed with Bcrypt and never stored or logged in plaintext. Passwords longer than 72 bytes are rejected, since Bcrypt truncates beyond that. Login performs a hash comparison even for unknown usernames, so response time does not reveal which usernames exist, and login is rate-limited.

**Authorization.** Role-Based Access Control is enforced at the Presentation Layer:

| Route type | Role check |
|:---|:---|
| Open (register patient, login) | None. |
| Patient and doctor routes (view slots, book, cancel, set own schedule) | Role claim from the token. No database lookup. |
| Admin routes (create doctor, delete account, record leave) | Role looked up in the database on every request. |

Ownership checks need data and are enforced in the Business Logic and in query filters (for example, cancelling requires the appointment to belong to the caller). The user ID is always taken from the token, never from the request body.

**Trade-off.** Stateless tokens cannot be revoked before they expire, so a deleted or demoted account keeps its token's claims until it lapses. The short expiry bounds this window, and its reach is small: a demoted administrator is still a doctor, and a deleted patient's actions are scoped to their own rows. Admin routes, the only high-privilege operations, re-verify the role against the database, so a revoked or demoted administrator loses privileged access immediately. Soft deletion supplies the revocation signal for that check.
