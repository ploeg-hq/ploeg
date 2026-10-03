## ADDED Requirements

### Requirement: Current guidance has a clear entry point

The documentation SHALL provide a human entry point and a machine discovery index linking to the same current Markdown and structured sources.

#### Scenario: A reader needs implemented behavior

- **WHEN** a reader starts from the documentation index
- **THEN** current architecture and contracts are identifiable separately from proposed design and historical evidence

### Requirement: An audit states its evidence and limits

The audit SHALL record baseline file hashes, disposition, review depth and findings. Classification alone MUST NOT be described as verification of every claim.

#### Scenario: An old claim is replaced

- **WHEN** a current page is corrected against implementation
- **THEN** the audit records the source evidence and retains access to the prior revision

### Requirement: Open choices remain open

Documentation MUST distinguish the product direction of Ploeg-free local work from the proposed shared execution implementation. It MUST NOT describe mandatory Ploeg authorization for all workloads or a shared runner as an accepted architecture decision.

#### Scenario: A reader considers local execution

- **WHEN** a reader consults the domain and landscape guides
- **THEN** the reader can identify standalone local work without Ploeg, explicit authority for managed work, and the shared runner as an unproven implementation proposal
