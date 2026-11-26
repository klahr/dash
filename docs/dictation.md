# Dictation

Speak instead of typing in Dash's prompt fields. To have Dash read Claude's replies aloud, see
[Spoken replies](#spoken-replies). To work hands-free, see [Voice mode](#voice-mode).

A mic button sits on:

- **Instructions for Claude** in the new task form
- the instructions you edit in an open task
- the chats: **Message Claude**, **Ask about this project** and the task chat
- a Planning card's comment box

It is off until you turn it on. It is a setting of this browser.

## Turn it on

1. Open **Account → Voice**.
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

1. Open **Account → Voice**.
2. Turn on **Voice commands**.

**Voice commands** shows in the top bar's **⋯** menu. Chrome, Edge and Safari have voice
commands; Firefox doesn't.

### Start and stop

- Press `Ctrl-Shift-,` (`⌘-Shift-,` on a Mac), or choose **⋯ → Voice commands** in the top bar.
  Allow the microphone the first time.
- A **Listening for commands** bar shows what Dash heard.
- Say "stop listening", press `Esc`, or click **Stop** (or **⋯ → Stop voice commands**) to end.
  Hiding the page or an error ends it too.

Press the shortcut while the cursor is in a chat, so "send" knows what to send.

### The commands

| Say                                   | Does                                                         |
|---------------------------------------|--------------------------------------------------------------|
| "send"                                | sends the chat or comment you are in; in **New task**, saves a draft (`Enter`) |
| "queue this task", "run this task"    | queues the task form, or the open draft, or the draft under the board's cursor (`s`) |
| "approve", "allow"                    | allows the permission request in view (`y`)                  |
| "deny"                                | denies it (`X`)                                              |
| "next card", "previous card"          | the next or previous task: the board's cursor (`j` `k`), or in an open task, the task beside it in its column |
| "read it all"                         | reads the last reply, request or task news Dash said in short, in full (see [Summaries](#summaries)) |
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

## Voice mode

Voice mode is a full-screen view for working without keyboard or mouse. It listens all the time
until you leave it, and Dash talks back. There is no wake word: starting voice mode is your OK to
listen.

Chrome, Edge and Safari have it. It needs no setting: Dash reads aloud in voice mode even with
**Spoken replies** off.

### Start and leave it

- Choose **⋯ → Start voice mode** in the top bar, or press `gv`. Allow the microphone the first
  time.
- To leave, say "exit voice mode", press `Esc`, or click **Leave**. The microphone and all speech
  stop.

Nothing listens outside voice mode, except dictation and voice commands when you start them.

### What it shows

In large text: the project and the open task with its state, what Dash is doing (**Listening**,
**Dictating**, **Say yes or no**, **Speaking**, **Paused**), what you dictate as you say it, the
last thing Dash said, and what you can say now.

A short sound plays on each change, so you know the state without looking: rising when it
listens, higher when you dictate, a double beep when it waits for yes or no, falling when it
pauses, low when it stops.

### What to say

Say a command on its own, after a pause.

| Say                              | Does                                                            |
|----------------------------------|-----------------------------------------------------------------|
| "new task"                       | dictate a new task for the current project                     |
| "dictate"                        | dictate a message: to the open task's side chat, or else the project's chat |
| "done", "send"                   | end dictating; Dash reads it back                              |
| "scratch that"                   | while dictating, takes the last part out                       |
| "cancel"                         | drops what you dictated                                         |
| "yes", "no"                      | answer a read-back. "no" goes back to dictating                 |
| "repeat"                         | says the last thing again                                       |
| "stop"                           | cuts Dash off                                                   |
| "read it all"                    | reads the last thing said in short, in full                     |
| "pause", "resume"                | stop and start acting on what you say. Paused, only "resume" and "exit voice mode" work |
| "help"                           | says what you can say now                                       |
| "exit voice mode"                | leaves voice mode                                               |

The [voice commands](#the-commands) work too: "approve", "deny", "queue this task", "next card",
"previous card", "open board" and the others. While you dictate, they are words, not commands.

### Read back before acting

Before Dash queues a task, sends a message or approves a permission request, it reads it back
and waits:

> Queue a new task: fix the login redirect. Say yes to confirm.

Say "yes" to do it. Anything else does nothing. Merging, deploying, deleting and rolling back
stay buttons you click.

A message goes on its own: what you typed or attached in the chat stays there. While Claude
answers, the message waits in the chat's queue and Dash says "Queued."

### Turn-taking

Dash says replies, approval requests and task results in short (see [Summaries](#summaries)).
While it speaks, it only hears "stop", "pause" and "exit voice mode", so it doesn't act on its own
voice. When it's done, it listens again. Press any key but `Esc` to cut it off.

If you speak while Dash is talking, wait for it to finish, or say "stop" first.

### Shared rooms

Use headphones. Dash then doesn't hear itself, and the people around you aren't heard as
commands. Pause voice mode ("pause") when you talk to someone.

### When it stops listening

The browser ends a listen after a silence or a minute, and on network errors. Voice mode listens
again on its own, waiting a little longer after each failure. It keeps listening while the page
is hidden, where the browser allows it.

It stops, and shows and says why, only when it can't go on: the microphone isn't allowed, there
is no microphone, or the browser has no speech recognition. Fix that, then start voice mode again.

## Privacy

The browser does the speech recognition for dictation, voice commands and voice mode, not Dash. Chrome
sends the audio to Google to transcribe it. Safari may transcribe it on the device. Dash only
gets the text you send.

## Spoken replies

Dash can read aloud, so you can follow a chat or a task without looking at the screen.

### Turn it on

1. Open **Account → Voice**.
2. Turn on **Spoken replies**. Dash says a short line in the voice it will use.
3. Under **Read aloud**, choose what to hear:
   - **Replies**: Claude's answers in **Message Claude**, **Ask about this project** and the task
     chat, when an answer is done.
   - **Approval requests**: what Claude wants to run, in short.
   - **Task status**: a task is ready for review, failed or finished. These are the tasks you hear
     about in **Account → Notifications**.
4. Leave **Summarise spoken replies** on to hear long text in short. See [Summaries](#summaries).
5. Pick a **Voice** and a **Speed** if you like. **Voice** shows when the browser has several.

It is off until you turn it on. It is a setting of this browser. Chrome, Edge, Safari and Firefox
have it. Where a browser doesn't, the setting is greyed out.

### What is read

Dash reads plain prose. It doesn't read code: a code block, a diff or a table is named, as in
"A code block, 12 lines". A long path is read as its file name. Markdown is left out.

A long reply is read up to its first part, then Dash says there's more. To hear all of every
reply, set **Long replies** to **Read all of it**.

Each reply has a speaker button in the row under it, which shows when you point at the reply (tap
it on a phone). Click it to hear the whole reply.

### Summaries

With **Summarise spoken replies** on, Dash says long text in a sentence or two instead of reading
it all:

- **Replies**: what Claude did or changed, and what it needs from you, if anything.
- **Approval requests**: the action and why, as a question: "Claude wants to run the agent
  package's tests, okay?"
- **Task status**: the result, and whether the tests passed.

Claude's small model writes them: the one for task titles (`-title-model`, Haiku by default). It
runs with your `claude` login, on your runner on a shared Dash. Text under about 40 words is read
as it is, and each message is summarised once.

If a summary fails, or takes more than 10 seconds, Dash reads the text as it would without
summaries.

To hear all of it, say "read it all" with [voice commands](#voice-commands) on, or in
[voice mode](#voice-mode). Dash reads the
last thing it said in short, in full, with code blocks named as above. Voice commands stop first,
so Dash doesn't hear itself. The speaker button under a reply reads it all too.

Summaries are on by default. Turn off **Summarise spoken replies** to hear the text itself.

### Stop it

- Press `Esc`.
- Click the speaker button again.
- Send a message.

Dash doesn't speak while dictation or voice commands listen, so it doesn't hear itself. Voice
mode takes turns instead: see [Turn-taking](#turn-taking).

### Privacy

The browser does the speaking, not Dash. Some voices are an online service, and the browser sends
it the text: Chrome's Google voices and Edge's online voices, for example. **Voice** marks these
*online*. Pick another voice to keep the text on your computer.

A summary sends the text to Claude, as a task or a chat does, with your own `claude` login.

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
