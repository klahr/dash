# Dictation

Speak instead of typing in Dash's prompt fields. To have Dash read Claude's replies aloud, see
[Spoken replies](#spoken-replies).

A mic button sits on:

- **Instructions for Claude** in the new task form
- the instructions you edit in an open task
- the chats: **Message Claude**, **Ask about this project** and the task chat
- a Planning card's comment box

It is off until you turn it on. It is a setting of this browser.

## Turn it on

1. Open **Account → Dictation**.
2. Turn on **Dictation**.

Chrome, Edge and Safari have it. Firefox doesn't, so the setting is greyed out there and no mic
button shows.

## Use it

- Click the mic, or press `Ctrl-Shift-.` (`⌘-Shift-.` on a Mac) in the field. Allow the
  microphone the first time.
- Speak. The field shows the words as they are heard.
- Click the mic again, or press `Esc`, to stop.

The words go in at the cursor, or over the selected text. What is already in the field stays.
Dash uses your browser's language.

Typing, leaving the field or closing the form also stops dictation, and keeps the words shown.

## Voice commands

Say a command and Dash runs it, as its buttons and keys do. Voice commands are off until you
turn them on, and are a setting of this browser.

### Turn them on

1. Open **Account → Dictation**.
2. Turn on **Voice commands**.

A mic shows in the top bar. Chrome, Edge and Safari have voice commands; Firefox doesn't.

### Start and stop

- Click the mic in the top bar, or press `Ctrl-Shift-,` (`⌘-Shift-,` on a Mac). Allow the
  microphone the first time.
- A **Listening for commands** bar shows what Dash heard.
- Say "stop listening", press `Esc`, or click **Stop** to end. Hiding the page or an error ends
  it too.

Click the mic or use the shortcut while the cursor is in a chat, so "send" knows what to send.

### The commands

| Say                                   | Does                                                         |
|---------------------------------------|--------------------------------------------------------------|
| "send"                                | sends the chat or comment you are in; in **New task**, saves a draft (`Enter`) |
| "queue this task", "run this task"    | queues the task form, or the open draft, or the draft under the board's cursor (`s`) |
| "approve", "allow"                    | allows the permission request in view (`y`)                  |
| "deny"                                | denies it (`X`)                                              |
| "next card", "previous card"          | the next or previous task: the board's cursor (`j` `k`), or in an open task, the task beside it in its column |
| "open board", "open chat"             | the **Board** (`gb`) / the project's chat (`c`)              |
| "open scans", "open history" …        | that section: board, scans, shell, history, goals, deploys, files, planning |
| "stop listening"                      | ends voice commands                                          |

Dash matches loosely: case, punctuation, "the", "this" and "please" don't matter. Speech that
isn't a command does nothing. "approve" answers a permission request; it doesn't approve or merge
a task in Review.

"send", "queue", "approve" and "deny" show what Dash heard and what it did in a message.

There are no voice commands that delete, merge, deploy or roll back.

### While dictating

With voice commands on, say "send" on its own, after a pause, to send a chat message or comment
you are dictating. The words before it stay in the field; "send" isn't typed. Say "stop
listening" on its own to stop dictating.

Said in the same breath as other words ("… and then send"), "send" is typed like any other
word. In the task instructions "send" is always typed, since a task can be saved or queued.

### Full hands-free control

Your operating system can control every button by voice, in every app:

- **macOS:** Voice Control, in **System Settings → Accessibility → Voice Control**.
- **Windows:** Voice Access, in **Settings → Accessibility → Speech**.

## Privacy

The browser does the speech recognition for dictation and voice commands, not Dash. Chrome
sends the audio to Google to transcribe it. Safari may transcribe it on the device. Dash only
gets the text you send.

## Spoken replies

Dash can read aloud, so you can follow a chat or a task without looking at the screen.

### Turn it on

1. Open **Account → Spoken replies**.
2. Turn on **Spoken replies**. Dash says a short line in the voice it will use.
3. Under **Read aloud**, choose what to hear:
   - **Replies**: Claude's answers in **Message Claude**, **Ask about this project** and the task
     chat, when an answer is done.
   - **Approval requests**: what Claude wants to run, in short.
   - **Task status**: a task is ready for review, failed or finished. These are the tasks you hear
     about in **Account → Notifications**.
4. Pick a **Voice** and a **Speed** if you like. **Voice** shows when the browser has several.

It is off until you turn it on. It is a setting of this browser. Chrome, Edge, Safari and Firefox
have it. Where a browser doesn't, the setting is greyed out.

### What is read

Dash reads plain prose. It doesn't read code: a code block, a diff or a table is named, as in
"A code block, 12 lines". A long path is read as its file name. Markdown is left out.

A long reply is read up to its first part, then Dash says there's more. To hear all of every
reply, set **Long replies** to **Read all of it**.

Each reply has a **Read aloud** button under it. Click it to hear the whole reply.

### Stop it

- Press `Esc`.
- Click **Read aloud** again.
- Send a message.

Dash doesn't speak while dictation or voice commands listen, so it doesn't hear itself.

### Privacy

The browser does the speaking, not Dash. Some voices are an online service, and the browser sends
it the text: Chrome's Google voices and Edge's online voices, for example. **Voice** marks these
*online*. Pick another voice to keep the text on your computer.

### Your system's screen reader

A screen reader reads the whole page, in every browser:

- **macOS and iPhone:** VoiceOver. Press `⌘-F5`, or turn it on in **Settings → Accessibility**.
- **Windows:** Narrator. Press `Win+Ctrl+Enter`.
- **Android:** TalkBack, in **Settings → Accessibility**.

## Your system's dictation

Your operating system's dictation works in every field and every browser, Firefox too:

- **macOS:** press `Fn` twice (Dictation, in **System Settings → Keyboard**).
- **Windows:** press `Win+H` (Voice typing).
- **Phones:** tap the mic key on the keyboard.
