---
title: "What Really Happens When You Run a Shell Command?"
date: 2026-06-15T13:31:48+07:00
author: "Ou David"
draft: false
tags: ["operating-systems", "unix", "processes", "systems-programming"]
categories: ["Technical Deep Dives"]
description: "How three UNIX process primitives make execution, redirection, and pipelines possible."
image: "images/thumbnails/process-api-shell.png"
toc: false
math: false
---

Every time I run a command like this:

```sh
grep "error" app.log | wc -l
```

it looks like a single operation: find lines containing `error`, then count them.

But the shell is doing much more than passing text between commands. It starts separate processes, connects their input and output, and coordinates what happens next. Two programs that know nothing about each other end up working together.

I recently read Chapter 5, The Process API, in Operating Systems: Three Easy Pieces. It helped me see how much of the UNIX shell comes down to three small ideas:

- `fork()` creates a new process.

- `exec()` replaces the program running inside a process.

- `wait()` lets a parent wait for a child to finish.

The interesting part isn’t just what each operation does. It’s why UNIX keeps them separate. This post follows the traditional fork-and-exec model for external commands. Shell builtins and alternative launch mechanisms can take different paths.

### First, `fork()` creates another process

Suppose a shell needs to run `wc`. It could use a single operation that creates and launches the new program. In the traditional fork-and-exec model, the shell first creates a child process.

After a successful `fork()`, both processes continue from the point where `fork()` returned. The child gets a different process ID and its own logical address space. Changes to a variable in the child won’t change the parent’s variable.

At first, I assumed the child would wait for the parent to tell it to start. It doesn’t. Both are eligible to run, and the scheduler decides when each one gets CPU time.

The child also inherits the parent’s open file descriptors. Those descriptors refer to the same underlying open file descriptions, which is why the two processes may initially write to the same terminal or file.

That inheritance turns out to be essential.

### Then, `exec()` turns it into another program

A forked child is initially running the same program as its parent. A shell normally wants something else: `grep`, `wc`, `ls`, or whatever command I typed.

So the child calls an exec-family function:

```c
char *args[] = {"wc", "-l", "app.log", NULL};
execvp(args[0], args);

// Reached only if execvp fails.
perror("execvp");
_exit(127);
```

The distinction matters: `exec()` does not create another process. It replaces the program image of the current one. The process retains its PID, but begins executing different code. Open file descriptors normally survive exec unless marked close-on-exec, which is why the redirection remains in place when the new program starts.

This gives the shell a useful sequence:

1. Call `fork()` to create a child.

2. Configure the child’s environment and file descriptors.

3. Call `exec()` to launch the requested program.

And here is the clever part: the shell gets a window to configure the child before the new program begins.

### That small window gives the shell a convenient place to set up redirection.

Consider:

```sh
wc -l app.log > out.txt
```

The `wc` program doesn’t need to know what `>` means. The shell handles that before it executes `wc`.

In the child, the essential redirection step looks like this:

```c
int fd = open("out.txt", O_WRONLY | O_CREAT | O_TRUNC, 0644);
if (fd < 0) { perror("open"); _exit(1); }

if (fd != STDOUT_FILENO) {
    if (dup2(fd, STDOUT_FILENO) < 0) {
        perror("dup2");
        _exit(1);
    }
    close(fd);
}

char *args[] = {"wc", "-l", "app.log", NULL};
execvp(args[0], args);
perror("execvp");
_exit(127);
```

`dup2()` makes standard output (file descriptor 1) refer to `out.txt`. When `wc` starts, it simply writes to standard output as usual. It doesn’t know that its output is going to a file.

The shell sets up the environment; the program just does its job.

### What about `wait()`?

Creating a process doesn’t guarantee which one runs first. Without explicit coordination, the parent and child might print in either order.

When the shell runs a foreground command, it normally needs to wait until that command finishes. The parent can use `wait()` or `waitpid()` for that synchronization:

```c
pid_t pid = fork();

if (pid == 0) {
    puts("child");
    exit(0);
} else if (pid > 0) {
    waitpid(pid, NULL, 0);
    puts("parent");
}
```

Here, the parent waits for the specific child before printing parent, assuming `waitpid()` succeeds. This simplified example omits error handling: `waitpid()` can fail or be interrupted by a caught signal, so production code should check its return value and retry on `EINTR`. Waiting also lets the parent collect the child’s termination status and reap the terminated child; this example passes `NULL` to discard the status.

That’s a useful lesson beyond operating systems: concurrent execution doesn’t imply an order. If the order matters, synchronization must be explicit.

### Pipes use exactly the same building blocks

Now back to the original command:

```sh
grep "error" app.log | wc -l
```

The shell creates a pipe and two child processes. It connects `grep`’s standard output to the pipe’s write end and `wc`’s standard input to its read end. Then each child executes its program.

```text
grep  --stdout-->  [ pipe ]  --stdin-->  wc -l
```

The shell also closes unused pipe ends so readers can detect end-of-file correctly, and waits for the foreground pipeline as needed. A reader reaches EOF after buffered data has been consumed and all descriptors referring to the pipe’s write end are closed, including inherited duplicates.

Neither command needs special knowledge of the other. `grep` writes bytes; `wc` reads bytes. The operating system provides the mechanism, and the shell assembles the pieces.

### Why this design stayed with me

Before reading this chapter, I thought of `fork()`, `exec()`, and `wait()` as separate low-level APIs to memorize. Now I see them as a design for composing programs without coupling them together.

The separation gives each operation a clear role in this traditional model:

- `fork()` gives the shell a child it can configure.

- `exec()` allows that child to become a different program.

- `wait()` provides coordination when the parent needs it.

From these primitives, we get everyday features such as foreground commands, output redirection, and pipelines.

The shell feels high-level, but its power comes from a small set of low-level operations with clear responsibilities. That’s the part of UNIX design I appreciate most.

Reading: [*Operating Systems: Three Easy Pieces*, Chapter 5 — The Process API](https://pages.cs.wisc.edu/~remzi/OSTEP/cpu-api.pdf).

For a runnable Go example, see [go/process-api-shell](https://gitlab.vvivid.dev/showcase/go-lang-case-study/-/tree/main/go/process-api-shell). It uses Go’s standard `os/exec` package to run commands, redirect output, build a simple pipeline, and wait for the processes to finish.
