# Appointment Management System - Savannah Informatics Limited

## Initial Considerations

The first task was to derive functional and non-functional requirements from the problem statement given. In doing this early, I was able to identify and describe assumptions early on, as well as providing myself a solid foundation on which to build upon the rest of the system's design.

### Assumptions
- Appointments last for only one time slot.
- Doctors are able to schedule breaks in their schedule as needed; and
- The schedule might not be uniform across all working days.
- Staff who operate the system are doctors.
- Doctors go on breaks, which in this case is capped at one hour.
- Payment integration is outside the scope

### Functional Requirements

1. Faculty(Doctor) management:
    - Doctors should be able to have and set their own working hours, for each day of the week.
    - Appointment slots should not overlap with both each other and designated Break Times.
    - Doctors are able to define their own Break Time
1. Patient Appointment Booking:
    - Patients should be able to view when a particular doctor is available
    - Patients should be able to book a specific slot with a particular doctor
    - Patients should be able to cancel their own appointments at will
    - Two patients should not book the same doctor for the same time slot
    - In case a patient misses an appointment the system should distinguish it from a completed appointment
1. Account management:
    - An administrator should be able to create staff (Doctor) accounts
    - An administrator should be able to delete any account at their discretion
    - Patients should be able to create their own accounts

### Non-Functional Requirements

1. The application should have a low latency
1. The application must have broad test coverage covering all functional requirements

## System Design

With the requirements in place, I could finally move on to the design of the application. Before settling on any technology, first I had to come up with the schema.

### Entity Relationships

From the requirements we can infer that there are two types of users who will interact with the system: Patients and Doctors. However, there is a significant overlap between the two entities, as both share fields for biographical and identifying information, which will be necessary for security and authentication. Thus, in the schema we create a `users` table that will store the common information.

