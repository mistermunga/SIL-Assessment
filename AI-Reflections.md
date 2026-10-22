# Use of AI in Development: Reflections

I have always been of the mind that Artificial Intelligence is an instrument to be used; it is a means of augmenting human capability and intelligence. Using AI as a tool has only made me a more competent and productive developer.

For this project, I used AI throughout the development cycle. However, it is worth noting that I did most of the design in the early stages, with AI use increasing downstream, once most of the major decisions had been made.

### Design

- I leaned on my instincts and experience to derive requirements and come up with the structure of the project. I then used AI as a sanity check to ensure I had not missed anything and that the concept had no blind spots.
- Where my design had gaps, I weighed the AI's suggestions and only implemented those I felt aligned with the derived requirements.

### Implementation

- My main tech stack is Java-based, so I had to transfer that expertise to a different language, with different conventions for familiar concepts. Here, AI was instrumental in translating the logic in my head into working code. It caught errors and guided me as I wrote code in a language to which I had little exposure.
- AI was also instrumental in writing the tests. Since I had a clear idea of what each file and module did, I was able to direct it to produce tests with broad coverage. I could not have created even half as many tests without AI.
- Thanks to these tests, I was able to find bugs in my logic and refine my design further.

### Deployment

- Despite my relative comfort with backend development, I was less familiar with Continuous Integration. The breadth of cloud technologies is its own barrier to entry, but AI helped me create a sensible pipeline that achieved the goals I had set.
- Beyond this, I encountered some minor snags during deployment. Initially, the Virtual Machine I had chosen had too little memory, causing the deployment to crash it. Using AI, I was able to troubleshoot and fix the problem.
- Lastly, I used AI to generate the documentation. I knew what a reviewer would need from the document, and once again, AI acted as a sanity check, identifying gaps and helping craft a polished final document.

I was involved in all stages, essentially acting as an overseer, so I maintained an intimate knowledge of every part of the project.

## AI Improvement

Early in the project, I developed a schema with doctors, patients, and appointments. I wanted to keep patients and doctors conceptually separate, even at a low level. The trade-off was that there would be considerable overlap between the two tables, and I would have to handle them separately. Still, I thought this was a reasonable approach, as clear boundaries would make the resulting application easier to design and implement. I was satisfied with the design, but still decided to run it past AI.

I gave it the scenario, the entities I wanted the schema to contain, the database-level checks I needed (such as the index that prevents double booking), and the database technology I intended to use. The AI convinced me that the benefits of my original approach did not justify the cost. At its suggestion, I introduced a `users` table, bringing the schema more in line with the entity relationships in the system. With the benefit of hindsight, I see that the benefits of leaving out the `users` table would have been minuscule.

## AI Correctness

Because I already knew what I wanted to build, the AI rarely made notable errors. The most notable was during the implementation of the scheduling subsystem, `internal/scheduling`. Initially, the AI suggested that doctors have two or more shifts split around their breaks. I caught this while reviewing the implementation of `window.go`. This would have increased the complexity of the schema without providing any meaningful benefit, and was therefore needless complexity.

## Human Decision-Making

Virtually all the design decisions in the project were my own, from the schema and architecture to the CI/CD pipeline. I chose to structure the application as a monolith. I decided how to split the requirements into services in the business logic layer, as well as how to set up routing at the presentation layer.

Though I come from a different tech stack, I wanted to lean on the knowledge I had gained through experience and learning. By adhering to best practices, I was able to ship a thoroughly tested, complete application. I trusted my own judgment here because I am better at retaining and applying context than a chatbox, and I knew that blind trust in AI would be an inferior approach.