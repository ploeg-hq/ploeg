# Context bundles

## ADDED Requirements

### Requirement: A person can attach files to a Work Item

ploegd SHALL accept a zip, a tar.gz or any single file for a Work Item through
the operator API, SHALL store each digest once per Work Item, and SHALL refuse
a file that breaks a size limit or an archive rule with the rule's name
(ADR-0067).

#### Scenario: A file is attached

- **WHEN** an operator posts a zip for a Work Item
- **THEN** it is stored with its digest, size, file count, name, note, adder and time

#### Scenario: The same file twice is one item

- **WHEN** the same file is posted again for the same Work Item
- **THEN** the existing item is returned and nothing new is stored

#### Scenario: An unsafe archive is refused at upload

- **WHEN** an archive holds a path with `..`, an absolute path or a link
- **THEN** it is refused with a reason naming the rule, and nothing is stored

#### Scenario: A zip bomb is refused

- **WHEN** an entry expands more than 200 times its compressed size
- **THEN** the archive is refused

#### Scenario: A finished Work Item takes no context

- **WHEN** context is posted for a Work Item that is done or withdrawn
- **THEN** it is refused as a conflict

### Requirement: A Run receives verified context

The claim SHALL carry references to every item added before it. The worker
SHALL download each with its Run capability, SHALL verify digest and size,
SHALL unpack it outside the clone, and SHALL list it in a "Context from people"
prompt section after the Work Item description.

#### Scenario: Context reaches the Run

- **GIVEN** a Work Item with an attached markdown file
- **WHEN** a Run is claimed
- **THEN** the file is in the Run's context directory and its name is in the prompt

#### Scenario: Tampered context stops the Run

- **WHEN** a downloaded item's digest does not match its reference
- **THEN** the Run goes stuck with a reason naming the item

#### Scenario: A Run reads only its own Work Item's context

- **WHEN** a Run asks for an item of another Work Item
- **THEN** the item is not found

### Requirement: Context added while steering reaches the next Run

An item added while its Work Item has an open Shift SHALL be marked
`while_steering` and SHALL reach the next Run claimed, never a Run already
running (ADR-0068).

#### Scenario: Steering context waits for the next Run

- **GIVEN** a Run claimed before an item was attached
- **WHEN** the next Run of the Shift is claimed
- **THEN** only the next Run's TaskSpec lists the item, marked as added while steering
