# Quick start

In this quick start guide, you will perform your first backup followed by a restore.

## Prerequisites

* Configure connection to your M365 Tenant (see [M365 Access](/setup/m365_access))
* Initialize a Corso backup repository (see [Repositories](/setup/repos))

## Your first backup

Corso can do much more, but you can start by creating a backup of your Exchange mailbox.

To do this, you can run the following command:

```bash
$ docker run -e CORSO_PASSPHRASE \
    --env-file ~/.corso/corso.env \
    -v ~/.corso:/app/corso corso/corso:<release tag> \
    backup create exchange --user <your exchange email address>

  Started At            ID                                    Status                Selectors
  2022-10-10T19:46:43Z  41e93db7-650d-44ce-b721-ae2e8071c728  Completed (0 errors)  alice@example.com
```

:::note
Your first backup may take some time if your mailbox is large.
:::

## Restore an email

Now lets explore how you can restore data from one of your backups.

You can see all Exchange backups available with the following command:

```bash
$ docker run -e CORSO_PASSPHRASE \
    --env-file ~/.corso/corso.env \
    -v ~/.corso:/app/corso corso/corso:<release tag> \
    backup list exchange 

  Started At            ID                                    Status                Selectors
  2022-09-09T42:27:16Z  72d12ef6-420a-15bd-c862-fd7c9023a014  Completed (0 errors)  alice@example.com
  2022-10-10T19:46:43Z  41e93db7-650d-44ce-b721-ae2e8071c728  Completed (0 errors)  alice@example.com
```

Select one of the available backups and search through its contents.

```bash
$ docker run -e CORSO_PASSPHRASE \
    --env-file ~/.corso/corso.env \
    -v ~/.corso:/app/corso corso/corso:<release tag> \
    backup details exchange \
    --backup <id of your selected backup> \
    --user <your exchange email address> \
    --email-subject <portion of subject of email you want to recover>
```

The output from the command above should display a list of any matching emails. Note the ID
of the one to use for testing restore.

When you are ready to restore, use the following command:

```bash
$ docker run -e CORSO_PASSPHRASE \
    --env-file ~/.corso/corso.env \
    -v ~/.corso:/app/corso corso/corso:<release tag> \
    backup details exchange \
    --backup <id of your selected backup> \
    --user <your exchange email address> \
    --email <id of your selected email>
```

You can now find the recovered email in a mailbox folder named `Corso_Restore_DD-MMM-YYYY_HH:MM:SS`.

You are now ready to explore the [Command Line Reference](cli/corso) and try everything that Corso can do.